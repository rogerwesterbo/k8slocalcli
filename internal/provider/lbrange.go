package provider

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/netip"
	"time"

	"github.com/rogerwesterbo/k8slocalcli/internal/engine"
)

// LoadBalancerRange returns an IPv4 range on the cluster's container network
// for LoadBalancer Services (MetalLB), or "" when it cannot be determined.
// Docker hands out container addresses from the bottom of a subnet, so the
// range is taken from near the top (see lbRange).
func LoadBalancerRange(ctx context.Context, p Provider, name string) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	subnets, err := engine.Current().ContainerSubnets(ctx, p.GatewayNode(name))
	if err != nil {
		return ""
	}
	for _, s := range subnets {
		if r := lbRange(s); r != "" {
			return r
		}
	}
	return ""
}

// lbRange picks 51 addresses just below the broadcast address of an IPv4
// subnet, leaving the last few free: 172.18.0.0/16 → 172.18.255.200-172.18.255.250,
// 10.5.0.0/24 → 10.5.0.200-10.5.0.250. Subnets smaller than /25 or not IPv4
// return "".
func lbRange(cidr string) string {
	pfx, err := netip.ParsePrefix(cidr)
	if err != nil || !pfx.Addr().Is4() || pfx.Bits() > 25 {
		return ""
	}
	base := pfx.Masked().Addr().As4()
	size := uint32(1) << (32 - pfx.Bits())
	broadcast := binary.BigEndian.Uint32(base[:]) + size - 1
	return fmt.Sprintf("%s-%s", ip4(broadcast-55), ip4(broadcast-5))
}

func ip4(v uint32) netip.Addr {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return netip.AddrFrom4(b)
}
