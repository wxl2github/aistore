#!/bin/bash
set -e
. /etc/profile.d/dfcpaths.sh
echo 'Pull latest DFC'
cd $DFCSRC
git fetch
git reset --hard origin/master
git status
if [ ! -z $1 ]; then
    echo Git checkout branch $1
    git checkout $1
fi

VERSION=`git describe --tags`
BUILD=`date +%FT%T%z`
echo "Cloud provider set to: ${CLDPROVIDER}"
GOBIN=$GOPATH/bin go install -tags="${CLDPROVIDER}" -ldflags "-w -s -X 'main.version=${VERSION}' -X 'main.build=${BUILD}'" setup/dfc.go
