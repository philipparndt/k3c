package runtime

import "testing"

func TestParseDefaultNetwork(t *testing.T) {
	// the CLI escapes the slash in the subnet
	out := `[{"configuration":{"name":"default","mode":"nat"},"id":"default",
	"status":{"ipv4Gateway":"192.168.65.1","ipv4Subnet":"192.168.65.0\/24","ipv6Subnet":"fdac::\/64"}}]`
	gw, subnet, err := parseDefaultNetwork(out)
	if err != nil {
		t.Fatal(err)
	}
	if gw != "192.168.65.1" || subnet.String() != "192.168.65.0/24" {
		t.Fatalf("got %s %s", gw, subnet)
	}

	for _, bad := range []string{
		``,
		`[]`,
		`[{"status":{"ipv4Gateway":"192.168.65.1","ipv4Subnet":"nope"}}]`,
		`[{"status":{"ipv4Gateway":"10.0.0.1","ipv4Subnet":"192.168.65.0/24"}}]`,
	} {
		if _, _, err := parseDefaultNetwork(bad); err == nil {
			t.Errorf("expected an error for %q", bad)
		}
	}
}
