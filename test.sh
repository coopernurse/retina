#!/bin/sh

set -e 

mkdir -p bin
go build -o bin/retina  retina.go
go build -o bin/backend ./integ/bin/backend.go
go test -v ./integ -gocheck.v
