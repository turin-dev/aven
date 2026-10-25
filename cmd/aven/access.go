package main

import (
	"fmt"
	"net/url"
)

// cmdEnvironmentRestrict는 환경을 제한하거나(Environment RBAC: 프로젝트 admin만 변수·로그·설정에 접근) 푼다.
// 이름을 주지 않으면 링크된 환경이다. 프로젝트 admin만 바꿀 수 있다(서버가 검사한다).
func cmdEnvironmentRestrict(c *config, l *link, restrict bool, args []string) {
	verb := "restrict"
	if !restrict {
		verb = "unrestrict"
	}
	if len(args) > 1 {
		die("usage: aven environment %s [name]", verb)
	}
	e := &envRef{ID: l.EnvironmentID, Name: l.Environment}
	if len(args) == 1 {
		if e = findEnv(c.project(l.ProjectID), args[0]); e == nil {
			die("environment %q not found", args[0])
		}
	}
	var out envRef
	if err := c.call("PATCH", "/environments/"+url.PathEscape(e.ID), map[string]bool{"restricted": restrict}, &out); err != nil {
		die("%v", err)
	}
	if out.Restricted {
		fmt.Printf("environment %s is restricted: only project admins can access its variables, logs and settings (git push deploys continue)\n", out.Name)
	} else {
		fmt.Printf("environment %s is no longer restricted\n", out.Name)
	}
}
