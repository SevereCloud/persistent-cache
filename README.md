# persistent-cache

[![Build Status](https://travis-ci.org/SevereCloud/persistent-cache.svg?branch=master)](https://travis-ci.org/SevereCloud/persistent-cache)
[![Go Report Card](https://goreportcard.com/badge/github.com/severecloud/persistent-cache)](https://goreportcard.com/report/github.com/severecloud/persistent-cache)
[![Documentation](https://godoc.org/github.com/severecloud/persistent-cache?status.svg)](http://godoc.org/github.com/severecloud/persistent-cache)
[![codecov](https://codecov.io/gh/SevereCloud/persistent-cache/branch/master/graph/badge.svg)](https://codecov.io/gh/SevereCloud/persistent-cache)
[![GitHub issues](https://img.shields.io/github/issues/severecloud/persistent-cache.svg)](https://github.com/severecloud/persistent-cache/issues)
[![license](https://img.shields.io/github/license/severecloud/persistent-cache.svg?maxAge=2592000)](https://github.com/severecloud/persistent-cache/LICENSE)

[go-cache](https://github.com/patrickmn/go-cache) is an in-memory key:value 
store/cache similar to memcached that issuitable for applications running on
a single machine. Its major advantage is that, being essentially a thread-safe
`map[string]interface{}` with expiration times, it doesn't need to serialize
or transmit its contents over the network.

Any object can be stored, for a given duration or forever, and the cache can be
safely used by multiple goroutines.

[persistent-cache](https://github.com/severecloud/persistent-cache) write cache
changes to the binlog file.


## Installation

`go get github.com/severecloud/persistent-cache`

## Usage

```go
package main

import (
	"fmt"

	pcache "github.com/severecloud/persistent-cache"
)

func main() {
	c, err := pcache.Load(pcache.NoExpiration, 0, "test")
	if err != nil {
		c, err = pcache.New(pcache.NoExpiration, 0, "test")
		if err != nil {
			panic(err)
		}
	}

	foo, found := c.Get("foo")
	if found {
		fmt.Println(foo)
	}

	c.SetDefault("foo", "bar")
}
```
