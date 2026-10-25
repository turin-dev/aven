package node

import "testing"

func TestParseNetworkInfo(t *testing.T) {
	out := `{"Name":"keel-env_x","Labels":{"keel.managed":"true"},"IPAM":{"Config":[{"Subnet":"fd00:1::/64","Gateway":"fd00:1::1"},{"Subnet":"172.18.0.0/16","Gateway":"172.18.0.1"}]}}`
	n, err := ParseNetworkInfo([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Subnets) != 2 || n.Gateways[0].String() != "172.18.0.1" || n.Gateways[1].String() != "fd00:1::1" {
		t.Fatalf("%+v", n)
	}
	if _, err := ParseNetworkInfo([]byte(`{"Labels":{},"IPAM":{"Config":[{"Subnet":"10.0.0.0/24","Gateway":"10.0.0.1"}]}}`)); err == nil {
		t.Fatal("unmanaged network accepted")
	}
	if _, err := ParseNetworkInfo([]byte(`{"Labels":{"keel.managed":"true"},"IPAM":{"Config":[]}}`)); err == nil {
		t.Fatal("network without gateway accepted")
	}
}
