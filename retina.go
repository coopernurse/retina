package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"github.com/coopernurse/retina/ws"
	"github.com/gorilla/mux"
	"gopkg.in/project-iris/iris-go.v1"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

///////////////////////////////////
// Config //
////////////

type RpcConf struct {
	Path    string
	Timeout int
}

type WsHubConf struct {
	Listen    string
	Heartbeat int
}

type Vhost struct {
	Hostnames []string
	Docroot   string
	Rpc       RpcConf
	Proxy     map[string]string
	Wshub     map[string]string
	Aliases   map[string]string
}

type Config struct {
	Listen        string
	Irisport      int
	Vhosts        map[string]Vhost
	Websockethubs map[string]WsHubConf
}

func loadConfig(filename string) (conf Config, err error) {
	b, err := ioutil.ReadFile(filename)
	if err != nil {
		return
	}

	err = json.Unmarshal(b, &conf)
	return
}

///////////////////////////////////
// Iris //
//////////

type IrisGateway struct {
	Conn    iris.Connection
	Timeout time.Duration
}

func (s *IrisGateway) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	app, ok := vars["app"]
	if app == "" || !ok {
		// TODO: set 500 status or return JSON-RPC error response
		log.Println("ERROR IrisGateway: No app provided on request. vars:", vars)
		return
	}

	buf := bytes.Buffer{}
	_, err := buf.ReadFrom(req.Body)
	if err != nil {
		// TODO: set 500 status or return JSON-RPC error response
		log.Println("ERROR IrisGateway: Cannot read POST data", err)
		return
	}

	resp, err := s.Conn.Request(app, buf.Bytes(), s.Timeout)
	if err != nil {
		// TODO: set 500 status or return JSON-RPC error response
		log.Println("ERROR IrisGateway: Error making request to app", app, "-", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(resp)
}

///////////////////////////////////
// HTTP //
//////////

func nameForHost(host string) string {
	if host == "" {
		return "default host"
	}
	return host
}

func addHostToRoute(host string, route *mux.Route) *mux.Route {
	if host != "" {
		route.Host(host)
	}
	return route
}

func addRpcHandler(r *mux.Router, host string, rpc RpcConf, relayConn *iris.Connection) {
	if rpc.Path != "" {
		if relayConn == nil {
			log.Println("Iris not enabled - skipping config for path:", rpc.Path)
			return
		}

		path := rpc.Path
		if path == "" {
			path = "/api/"
		}
		if !strings.HasSuffix(path, "/") {
			path += "/"
		}
		path += "{app}"

		log.Println("Configuring", nameForHost(host), "with RPC path:", path)
		handler := &IrisGateway{
			Conn:    *relayConn,
			Timeout: time.Second * time.Duration(rpc.Timeout),
		}
		addHostToRoute(host, r.Handle(path, handler)).Methods("POST")
	}
}

func addWsHubHandler(r *mux.Router, host string, paths map[string]string, wsHubs map[string]*retinaws.External) {
	for path, wshubName := range paths {
		if !strings.HasSuffix(path, "/") {
			path += "/"
		}
		path += "{queue}"

		gateway, ok := wsHubs[wshubName]
		if ok {
			log.Println("Configuring", nameForHost(host), "with WsHub path:", path)
			addHostToRoute(host, r.Handle(path, gateway)).Methods("GET", "POST", "PUT", "HEAD", "DELETE")
		} else {
			log.Println("Error: No websockethubs found with name:", wshubName)
		}
	}
}

func addStaticHandler(r *mux.Router, host, docroot string, aliases map[string]string) {
	for alias, aliasroot := range aliases {
		log.Println("Adding alias", nameForHost(host), alias, " with docroot:", aliasroot)
		h := http.FileServer(http.Dir(aliasroot))
		addHostToRoute(host, r.PathPrefix(alias).HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			req.URL.Path = req.URL.Path[len(alias):]
			h.ServeHTTP(w, req)
		}))
	}
	log.Println("Configuring", nameForHost(host), "with docroot:", docroot)
	addHostToRoute(host, r.PathPrefix("/").Handler(http.FileServer(http.Dir(docroot))))
}

func addProxyHandlers(r *mux.Router, host string, proxy map[string]string) {
	for path, endpoint := range proxy {
		u, err := url.Parse(endpoint)
		if err != nil {
			panic(err)
		}
		log.Println("Proxying", nameForHost(host), path, "to:", endpoint)
		proxy := httputil.NewSingleHostReverseProxy(u)
		proxytrans := &http.Transport{Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true}
		proxy.Transport = proxytrans
		addHostToRoute(host, r.PathPrefix(path).Handler(proxy)).Methods("GET", "POST", "PUT", "HEAD", "DELETE")
	}
}

func addVhost(r *mux.Router, vhost Vhost, isDefault bool, relayConn *iris.Connection, wsHubs map[string]*retinaws.External) {
	for _, host := range vhost.Hostnames {
		addRpcHandler(r, host, vhost.Rpc, relayConn)
		addProxyHandlers(r, host, vhost.Proxy)
		addWsHubHandler(r, host, vhost.Wshub, wsHubs)

		// this must be last - will serve all other paths
		addStaticHandler(r, host, vhost.Docroot, vhost.Aliases)
	}

	if isDefault {
		addRpcHandler(r, "", vhost.Rpc, relayConn)
		addProxyHandlers(r, "", vhost.Proxy)
		addWsHubHandler(r, "", vhost.Wshub, wsHubs)
		addStaticHandler(r, "", vhost.Docroot, vhost.Aliases)
	}
}

func initRouter(conf Config, relayConn *iris.Connection, wsHubs map[string]*retinaws.External) *mux.Router {
	r := mux.NewRouter()

	addDefault := false
	for name, vhost := range conf.Vhosts {
		if name == "default" {
			addDefault = true
		} else {
			addVhost(r, vhost, false, relayConn, wsHubs)
		}
	}

	if addDefault {
		addVhost(r, conf.Vhosts["default"], true, relayConn, wsHubs)
	}

	return r
}

func serveHTTP(conf Config, router *mux.Router) error {
	http.Handle("/", router)
	return http.ListenAndServe(conf.Listen, nil)
}

func dialRelay(conf Config) (*iris.Connection, error) {
	conn, err := iris.Connect(conf.Irisport)
	if err != nil {
		return nil, err
	}

	return conn, nil
}

///////////////////////////////////

func main() {
	var cfile string
	flag.StringVar(&cfile, "c", "retina.json", "Configuration file (JSON)")
	flag.Parse()

	conf, err := loadConfig(cfile)
	if err != nil {
		log.Fatalln("Unable to load config:", cfile, err)
	}

	log.Println("Got config:", conf)

	wsHubs := make(map[string]*retinaws.External)
	for name, wsconf := range conf.Websockethubs {
		internalHttp := retinaws.NewInternal()
		wsHubs[name] = &retinaws.External{Router: internalHttp.Router, Timeout: 30 * time.Second}

		go func() {
			log.Println("WS Listiner starting on:", wsconf.Listen)
			err := http.ListenAndServe(wsconf.Listen, internalHttp)
			if err != nil {
				log.Fatalln("Unable to start ws listener", wsconf, "-", err)
			}
		}()
	}

	var relayConn *iris.Connection
	if conf.Irisport > 0 {
		relayConn, err := dialRelay(conf)
		if err != nil {
			log.Fatalln("Unable to connect to Iris relay on port", conf.Irisport, "-", err)
		}
		defer relayConn.Close()
	}

	router := initRouter(conf, relayConn, wsHubs)

	log.Println("HTTP server listening on:", conf.Listen)
	err = serveHTTP(conf, router)
	if err != nil {
		log.Fatalln("Error in serveHTTP:", err)
	}

}
