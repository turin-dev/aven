package api

import (
	"regexp"
	"sync"

	"github.com/turin-dev/aven/internal/logbook"
)

type logbookEvent = logbook.Event

var reCache sync.Map

func regexpCache(re string) *regexp.Regexp {
	if v, ok := reCache.Load(re); ok {
		return v.(*regexp.Regexp)
	}
	r := regexp.MustCompile(re)
	reCache.Store(re, r)
	return r
}
