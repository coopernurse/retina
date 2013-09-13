package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"github.com/gorilla/mux"
	"github.com/karalabe/iris-go"
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

type Vhost struct {
	Hostnames []string
	Docroot   string
	Rpc       RpcConf
	Proxy     map[string]string
}

type Config struct {
	Listen   string
	Irisport int
	Vhosts   map[string]Vhost
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

func addRpcHandler(r *mux.Router, host string, rpc RpcConf, relayConn iris.Connection) {
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
			Conn:    relayConn,
			Timeout: time.Second * time.Duration(rpc.Timeout),
		}
		addHostToRoute(host, r.Handle(path, handler)).Methods("POST")
	}
}

func addStaticHandler(r *mux.Router, host, docroot string) {
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
		addHostToRoute(host, r.PathPrefix(path).Handler(httputil.NewSingleHostReverseProxy(u))).Methods("GET", "POST", "PUT", "HEAD", "DELETE")
	}
}

func addVhost(r *mux.Router, vhost Vhost, isDefault bool, relayConn iris.Connection) {
	for _, host := range vhost.Hostnames {
		addRpcHandler(r, host, vhost.Rpc, relayConn)
		addProxyHandlers(r, host, vhost.Proxy)

		// this must be last - will serve all other paths
		addStaticHandler(r, host, vhost.Docroot)
	}

	if isDefault {
		addRpcHandler(r, "", vhost.Rpc, relayConn)
		addProxyHandlers(r, "", vhost.Proxy)
		addStaticHandler(r, "", vhost.Docroot)
	}
}

func initRouter(conf Config, relayConn iris.Connection) *mux.Router {
	r := mux.NewRouter()

	addDefault := false
	for name, vhost := range conf.Vhosts {
		if name == "default" {
			addDefault = true
		} else {
			addVhost(r, vhost, false, relayConn)
		}
	}

	if addDefault {
		addVhost(r, conf.Vhosts["default"], true, relayConn)
	}

	return r
}

func serveHTTP(conf Config, relayConn iris.Connection) error {
	http.Handle("/", initRouter(conf, relayConn))
	return http.ListenAndServe(conf.Listen, nil)
}

func dialRelay(conf Config) (iris.Connection, error) {
	conn, err := iris.Connect(conf.Irisport, "", nil)
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

	var relayConn iris.Connection
	if conf.Irisport > 0 {
		relayConn, err := dialRelay(conf)
		if err != nil {
			log.Fatalln("Unable to connect to Iris relay on port", conf.Irisport, "-", err)
		}
		defer relayConn.Close()
	}

	log.Println("HTTP server listening on:", conf.Listen)
	err = serveHTTP(conf, relayConn)
	if err != nil {
		log.Fatalln("Error in serveHTTP:", err)
	}

}
