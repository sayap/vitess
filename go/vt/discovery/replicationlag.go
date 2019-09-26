/*
Copyright 2017 Google Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreedto in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package discovery

import (
	"flag"
	"fmt"
	"sort"
	"time"
)

var (
	// lowReplicationLag defines the duration that replication lag is low enough that the VTTablet is considered healthy.
	lowReplicationLag            = flag.Duration("discovery_low_replication_lag", 30*time.Second, "the replication lag that is considered low enough to be healthy")
	highReplicationLagMinServing = flag.Duration("discovery_high_replication_lag_minimum_serving", 2*time.Hour, "the replication lag that is considered too high when selecting the minimum num vttablets for serving")
	minNumTablets                = flag.Int("min_number_serving_vttablets", 2, "the minimum number of vttablets that will be continue to be used even with low replication lag")
)

// IsReplicationLagHigh verifies that the given TabletStats refers to a tablet with high
// replication lag, i.e. higher than the configured discovery_low_replication_lag flag.
func IsReplicationLagHigh(tabletStats *TabletStats) bool {
	return float64(tabletStats.Stats.SecondsBehindMaster) > lowReplicationLag.Seconds()
}

// IsReplicationLagVeryHigh verifies that the given TabletStats refers to a tablet with very high
// replication lag, i.e. higher than the configured discovery_high_replication_lag_minimum_serving flag.
func IsReplicationLagVeryHigh(tabletStats *TabletStats) bool {
	return float64(tabletStats.Stats.SecondsBehindMaster) > highReplicationLagMinServing.Seconds()
}

// FilterByReplicationLag filters the list of TabletStats by TabletStats.Stats.SecondsBehindMaster.
// The algorithm (TabletStats that is non-serving or has error is ignored):
// - Return tablets that have lag <= lowReplicationLag.
// - Make sure we return at least minNumTablets tablets, if there are enough one with lag <= highReplicationLagMinServing.
// For example, with the default of 30s / 2h / 2, this means:
// - lags of (5s, 10s, 15s, 120s) return the first three
// - lags of (30m, 35m, 40m, 45m) return the first two
// - lags of (2h, 3h, 4h, 5h) return the first one
//
// One thing to know about this code: vttablet also has a couple flags that impact the logic here:
// * unhealthy_threshold: if replication lag is higher than this, a tablet will be reported as unhealhty.
//   The default for this is 2h, same as the discovery_high_replication_lag_minimum_serving here.
// * degraded_threshold: this is only used by vttablet for display. It should match
//   discovery_low_replication_lag here, so the vttablet status display matches what vtgate will do of it.
func FilterByReplicationLag(tabletStatsList []*TabletStats) []*TabletStats {
	list := make([]tabletLagSnapshot, 0, len(tabletStatsList))
	// filter non-serving tablets and those with very high replication lag
	for _, ts := range tabletStatsList {
		if !ts.Serving || ts.LastError != nil || ts.Stats == nil || IsReplicationLagVeryHigh(ts) {
			continue
		}
		// Pull the current replication lag for a stable sort later.
		list = append(list, tabletLagSnapshot{
			ts:     ts,
			replag: ts.Stats.SecondsBehindMaster})
	}

	// Sort by replication lag.
	sort.Sort(byReplag(list))

	// Pick those with low replication lag, but at least minNumTablets tablets regardless.
	res := make([]*TabletStats, 0, len(list))
	for i := 0; i < len(list); i++ {
		if !IsReplicationLagHigh(list[i].ts) || i < *minNumTablets {
			res = append(res, list[i].ts)
		}
	}
	return res
}

func min(a, b int) int {
	if a > b {
		return b
	}
	return a
}

type tabletLagSnapshot struct {
	ts     *TabletStats
	replag uint32
}
type byReplag []tabletLagSnapshot

func (a byReplag) Len() int           { return len(a) }
func (a byReplag) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a byReplag) Less(i, j int) bool { return a[i].replag < a[j].replag }

// mean calculates the mean value over the given list,
// while excluding the item with the specified index.
func mean(tabletStatsList []*TabletStats, idxExclude int) (uint64, error) {
	var sum uint64
	var count uint64
	for i, ts := range tabletStatsList {
		if i == idxExclude {
			continue
		}
		sum = sum + uint64(ts.Stats.SecondsBehindMaster)
		count++
	}
	if count == 0 {
		return 0, fmt.Errorf("empty list")
	}
	return sum / count, nil
}

// TrivialStatsUpdate returns true iff the old and new TabletStats
// haven't changed enough to warrant re-calling FilterByReplicationLag.
func TrivialStatsUpdate(o, n *TabletStats) bool {
	// Skip replag filter when replag remains in the low rep lag range,
	// which should be the case majority of the time.
	lowRepLag := lowReplicationLag.Seconds()
	oldRepLag := float64(o.Stats.SecondsBehindMaster)
	newRepLag := float64(n.Stats.SecondsBehindMaster)
	if oldRepLag <= lowRepLag && newRepLag <= lowRepLag {
		return true
	}

	// Skip replag filter when replag remains in the high rep lag range,
	// and did not change beyond +/- 10%.
	// when there is a high rep lag, it takes a long time for it to reduce,
	// so it is not necessary to re-calculate every time.
	// In that case, we won't save the new record, so we still
	// remember the original replication lag.
	if oldRepLag > lowRepLag && newRepLag > lowRepLag && newRepLag < oldRepLag*1.1 && newRepLag > oldRepLag*0.9 {
		return true
	}

	return false
}
