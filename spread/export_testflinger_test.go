package spread

import "time"

var BuildProvisionData = buildProvisionData

func FakeTestFlingerRetryDelays(delays []time.Duration) (restore func()) {
	old := testFlingerRetryDelays
	testFlingerRetryDelays = delays
	return func() { testFlingerRetryDelays = old }
}

func DoTestFlingerRequest(p Provider, method, subpath string, result interface{}) error {
	return p.(*TestFlingerProvider).do(method, subpath, nil, result)
}
