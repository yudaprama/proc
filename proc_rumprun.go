// +build rumprun

package proc

func Usage(pcpu *float64, rss, vss *int64) error {
	*pcpu = 0.0
	*rss = 0
	*vss = 0

	return nil
}
