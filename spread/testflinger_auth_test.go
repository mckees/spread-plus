package spread_test

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"

	"github.com/canonical/spread-plus/spread"

	. "gopkg.in/check.v1"
)

type TestFlingerAuthSuite struct {
	savedEnv map[string]string
}

var _ = Suite(&TestFlingerAuthSuite{})

var testflingerAuthEnvKeys = []string{
	"TF_ENDPOINT",
	"TF_API_VERSION",
	"TESTFLINGER_CLIENT_ID",
	"TESTFLINGER_SECRET_KEY",
}

func (s *TestFlingerAuthSuite) SetUpTest(c *C) {
	s.savedEnv = make(map[string]string)
	for _, k := range testflingerAuthEnvKeys {
		s.savedEnv[k], _ = os.LookupEnv(k)
		os.Unsetenv(k)
	}
}

func (s *TestFlingerAuthSuite) TearDownTest(c *C) {
	for _, k := range testflingerAuthEnvKeys {
		if v := s.savedEnv[k]; v != "" {
			os.Setenv(k, v)
		} else {
			os.Unsetenv(k)
		}
	}
}

func (s *TestFlingerAuthSuite) setEnv(endpoint, clientID, secretKey string) {
	os.Setenv("TF_ENDPOINT", endpoint)
	os.Setenv("TF_API_VERSION", "v1")
	if clientID != "" {
		os.Setenv("TESTFLINGER_CLIENT_ID", clientID)
	}
	if secretKey != "" {
		os.Setenv("TESTFLINGER_SECRET_KEY", secretKey)
	}
}

func (s *TestFlingerAuthSuite) TestFetchTokenSendsBasicAuth(c *C) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.Check(r.Method, Equals, "POST")
		c.Check(r.URL.Path, Equals, "/v1/oauth2/token")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"jwt-abc","refresh_token":"refresh-xyz"}`))
	}))
	defer srv.Close()

	s.setEnv(srv.URL, "my-client", "my-secret")

	token, err := spread.FetchTestflingerToken(&http.Client{}, "my-client", "my-secret")
	c.Assert(err, IsNil)
	c.Check(token, Equals, "jwt-abc")

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("my-client:my-secret"))
	c.Check(gotAuth, Equals, want)
}

func (s *TestFlingerAuthSuite) TestFetchTokenErrorOnNon200(c *C) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"bad credentials"}`))
	}))
	defer srv.Close()

	s.setEnv(srv.URL, "my-client", "my-secret")

	_, err := spread.FetchTestflingerToken(&http.Client{}, "my-client", "my-secret")
	c.Assert(err, ErrorMatches, "cannot authenticate with TestFlinger .status 403.*bad credentials.*")
}

func (s *TestFlingerAuthSuite) TestEnsureAuthTokenCachesJWT(c *C) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"jwt-cached"}`))
	}))
	defer srv.Close()

	s.setEnv(srv.URL, "my-client", "my-secret")

	p := spread.NewTestFlingerProviderForTest(&http.Client{})
	c.Assert(p.EnsureAuthToken(), IsNil)
	c.Assert(p.EnsureAuthToken(), IsNil)

	c.Check(p.CurrentAuthToken(), Equals, "jwt-cached")
	c.Check(calls, Equals, 1)

	req, err := http.NewRequest("GET", srv.URL, nil)
	c.Assert(err, IsNil)
	p.SetAuthHeaderForTest(req)
	c.Check(req.Header.Get("Authorization"), Equals, "Bearer jwt-cached")
}

func (s *TestFlingerAuthSuite) TestEnsureAuthTokenNoCredentialsIsNoop(c *C) {
	s.setEnv("https://testflinger.example.com", "", "")

	p := spread.NewTestFlingerProviderForTest(&http.Client{})
	c.Assert(p.EnsureAuthToken(), IsNil)
	c.Check(p.CurrentAuthToken(), Equals, "")

	req, err := http.NewRequest("GET", "https://testflinger.example.com", nil)
	c.Assert(err, IsNil)
	p.SetAuthHeaderForTest(req)
	c.Check(req.Header.Get("Authorization"), Equals, "")
}

func (s *TestFlingerAuthSuite) TestCredentialsRequireBoth(c *C) {
	s.setEnv("https://testflinger.example.com", "only-client", "")
	_, _, ok := spread.TestflingerCredentials()
	c.Check(ok, Equals, false)
}

func (s *TestFlingerAuthSuite) TestDoRefreshesTokenOn401(c *C) {
	var tokenCalls, jobCalls int
	var bearers []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/oauth2/token":
			tokenCalls++
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"jwt-` + itoa(tokenCalls) + `"}`))
		case "/v1/job/123":
			jobCalls++
			bearers = append(bearers, r.Header.Get("Authorization"))
			// First attempt: reject with 401 to force a token refresh.
			if jobCalls == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"message":"expired"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"job_state":"complete"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s.setEnv(srv.URL, "my-client", "my-secret")

	p := spread.NewTestFlingerProviderForTest(&http.Client{})
	var result map[string]interface{}
	err := p.DoForTest("GET", "/job/123", nil, &result)
	c.Assert(err, IsNil)

	c.Check(tokenCalls, Equals, 2) // initial auth + one refresh on 401
	c.Check(jobCalls, Equals, 2)   // original + replay
	c.Check(bearers, DeepEquals, []string{"Bearer jwt-1", "Bearer jwt-2"})
	c.Check(result["job_state"], Equals, "complete")
}

func (s *TestFlingerAuthSuite) TestDoErrorsOnPersistent401(c *C) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth2/token" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"jwt-x"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"forbidden queue"}`))
	}))
	defer srv.Close()

	s.setEnv(srv.URL, "my-client", "my-secret")

	p := spread.NewTestFlingerProviderForTest(&http.Client{})
	err := p.DoForTest("GET", "/job/123", nil, nil)
	c.Assert(err, ErrorMatches, ".*unauthorized .status 401.*forbidden queue.*")
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
