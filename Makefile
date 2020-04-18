PROJECTNAME=kube-debug-pod
GOBIN=./bin
GOCMD=./cmd

default: build

clean:
	  rm -f $(GOBIN)/${PROJECTNAME}

mod:
	  go mod download

build:
	  go build -o $(GOBIN)/$(PROJECTNAME) $(GOCMD)/$(PROJECTNAME)/main.go

build_static: export CGO_ENABLED=0
build_static: build
