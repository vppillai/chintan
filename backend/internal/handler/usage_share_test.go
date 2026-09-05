package handler

import "testing"

// A tenant's share of the instance's AWS cost is a fraction of it, never more.
// The denominator — the instance's daily spend rows — expires after ninety
// days while the tenant's month row does not, so for an older month the raw
// quotient can exceed one; the share is then the whole cost, not more than it.
func TestShareOfIsClampedToTheWholeMonth(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		aws, tenant, instance int64
		want                  int64
		ok                    bool
	}{
		{"half", 10_000_000, 3, 6, 5_000_000, true},
		{"rounds half up", 10_000_000, 1, 3, 3_333_333, true},
		{"the whole", 10_000_000, 6, 6, 10_000_000, true},
		{"denominator expired under the tenant's total", 10_000_000, 9, 6, 10_000_000, true},
		{"instance spent nothing", 10_000_000, 0, 0, 0, false},
		{"instance rows all expired, tenant still counted", 10_000_000, 5, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := shareOf(tc.aws, tc.tenant, tc.instance)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("shareOf(%d, %d, %d) = %d, %v; want %d, %v", tc.aws, tc.tenant, tc.instance, got, ok, tc.want, tc.ok)
			}
		})
	}
}
