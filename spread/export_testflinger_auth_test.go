package spread

import "net/http"

var (
	FetchTestflingerToken  = fetchTestflingerToken
	TestflingerCredentials = testflingerCredentials
)

// NewTestFlingerProviderForTest builds a provider wired to the given HTTP
// client so tests can exercise the authentication flow.
func NewTestFlingerProviderForTest(client *http.Client) *TestFlingerProvider {
	return &TestFlingerProvider{client: client}
}

func (p *TestFlingerProvider) EnsureAuthToken() error { return p.ensureAuthToken() }

func (p *TestFlingerProvider) CurrentAuthToken() string { return p.currentAuthToken() }

func (p *TestFlingerProvider) SetAuthHeaderForTest(req *http.Request) { p.setAuthHeader(req) }

// DoForTest exposes the internal do() request helper so tests can exercise the
// end-to-end authentication and 401-retry behavior.
func (p *TestFlingerProvider) DoForTest(method, subpath string, params, result interface{}) error {
	return p.do(method, subpath, params, result)
}
