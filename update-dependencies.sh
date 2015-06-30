#!/bin/sh

# Updates submodules

git submodule foreach git fetch
# Assumes that all submodules use the master branch
git submodule foreach git rebase origin/master master
