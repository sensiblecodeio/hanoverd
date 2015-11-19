#! /bin/sh

VERSION=$(git describe --tags --abbrev=0)

cat > version.go <<EOF
package main

var Version string = "$VERSION"
EOF

# Dear git, please don't bother us with changes to this file.
git update-index --assume-unchanged version.go
