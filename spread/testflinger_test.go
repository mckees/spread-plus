package spread_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"github.com/canonical/spread-plus/spread"

	. "gopkg.in/check.v1"
)

type TestFlingerSuite struct{}

var _ = Suite(&TestFlingerSuite{})

func (s *TestFlingerSuite) TestBuildProvisionDataImageURL(c *C) {
	system := &spread.System{
		Name:  "ubuntu-classic",
		Image: "https://example.com/ubuntu.img.xz",
	}
	pdata, err := spread.BuildProvisionData(system)
	c.Assert(err, IsNil)
	c.Assert(pdata, DeepEquals, map[string]interface{}{
		"url": "https://example.com/ubuntu.img.xz",
	})
}

func (s *TestFlingerSuite) TestBuildProvisionDataImageDistro(c *C) {
	system := &spread.System{
		Name:  "ubuntu-classic",
		Image: "core22-latest-stable",
	}
	pdata, err := spread.BuildProvisionData(system)
	c.Assert(err, IsNil)
	c.Assert(pdata, DeepEquals, map[string]interface{}{
		"distro": "core22-latest-stable",
	})
}

func (s *TestFlingerSuite) TestBuildProvisionDataNoImage(c *C) {
	system := &spread.System{
		Name:  "ubuntu-classic",
		Image: "ubuntu-classic",
	}
	pdata, err := spread.BuildProvisionData(system)
	c.Assert(err, IsNil)
	c.Assert(pdata, IsNil)
}

func (s *TestFlingerSuite) TestBuildProvisionDataEmptyImage(c *C) {
	system := &spread.System{
		Name: "ubuntu-classic",
	}
	pdata, err := spread.BuildProvisionData(system)
	c.Assert(err, IsNil)
	c.Assert(pdata, IsNil)
}

func (s *TestFlingerSuite) TestBuildProvisionDataExplicit(c *C) {
	provisionData := map[string]interface{}{
		"distro":    "jammy",
		"kernel":    "hwe-22.04",
		"user_data": "#cloud-config\n",
	}
	system := &spread.System{
		Name:          "ubuntu-classic",
		ProvisionData: provisionData,
	}
	pdata, err := spread.BuildProvisionData(system)
	c.Assert(err, IsNil)
	c.Assert(pdata, DeepEquals, provisionData)
}

func (s *TestFlingerSuite) TestBuildProvisionDataImageAndProvisionDataFails(c *C) {
	provisionData := map[string]interface{}{
		"url":          "https://example.com/override.img.xz",
		"token_file":   "/run/token",
		"redeploy_cfg": "reset",
	}
	system := &spread.System{
		Name:          "ubuntu-classic",
		Image:         "https://example.com/ignored.img.xz",
		ProvisionData: provisionData,
	}
	// Setting both image and provision-data is ambiguous and must fail.
	pdata, err := spread.BuildProvisionData(system)
	c.Assert(pdata, IsNil)
	c.Assert(err, ErrorMatches, "system ubuntu-classic sets both image and provision-data; set only one")
}

// fakeTestFlinger serves the token endpoint and /v1/result/job, applying fail
// to every request first; fail returns true when it has handled the request.
func fakeTestFlinger(c *C, fail func(w http.ResponseWriter, r *http.Request) bool) (spread.Provider, func()) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail(w, r) {
			return
		}
		switch r.URL.Path {
		case "/v1/oauth2/token":
			w.Write([]byte(`{"access_token": "token"}`))
		case "/v1/result/job":
			c.Check(r.Header.Get("Authorization"), Equals, "Bearer token")
			w.Write([]byte(`{"job_state": "waiting"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	restoreDelays := spread.FakeTestFlingerRetryDelays([]time.Duration{0, 0, 0})
	restoreTimeout := spread.FakeTestFlingerRequestTimeout(200 * time.Millisecond)
	oldEndpoint, hadEndpoint := os.LookupEnv("TF_ENDPOINT")
	os.Setenv("TF_ENDPOINT", server.URL)
	p := spread.TestFlinger(&spread.Project{}, &spread.Backend{}, &spread.Options{})
	return p, func() {
		if hadEndpoint {
			os.Setenv("TF_ENDPOINT", oldEndpoint)
		} else {
			os.Unsetenv("TF_ENDPOINT")
		}
		restoreTimeout()
		restoreDelays()
		server.Close()
	}
}

// dropConnection makes the client see a network error, as during an outage.
func dropConnection(c *C, w http.ResponseWriter) {
	conn, _, err := w.(http.Hijacker).Hijack()
	c.Assert(err, IsNil)
	conn.Close()
}

func (s *TestFlingerSuite) TestDoRetriesNetworkErrors(c *C) {
	hits := map[string]int{}
	p, cleanup := fakeTestFlinger(c, func(w http.ResponseWriter, r *http.Request) bool {
		hits[r.URL.Path]++
		if hits[r.URL.Path] <= 2 {
			dropConnection(c, w)
			return true
		}
		return false
	})
	defer cleanup()

	var result map[string]interface{}
	err := spread.DoTestFlingerRequest(p, "GET", "/result/job", &result)
	c.Assert(err, IsNil)
	c.Assert(result["job_state"], Equals, "waiting")
	c.Assert(hits, DeepEquals, map[string]int{"/v1/oauth2/token": 3, "/v1/result/job": 3})
}

func (s *TestFlingerSuite) TestDoRetriesUnansweredRequests(c *C) {
	attempts := 0
	p, cleanup := fakeTestFlinger(c, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/v1/result/job" {
			return false
		}
		attempts++
		if attempts == 1 {
			// Hold the request past the client timeout, as a hung server does.
			time.Sleep(time.Second)
			return true
		}
		return false
	})
	defer cleanup()

	var result map[string]interface{}
	err := spread.DoTestFlingerRequest(p, "GET", "/result/job", &result)
	c.Assert(err, IsNil)
	c.Assert(result["job_state"], Equals, "waiting")
	c.Assert(attempts, Equals, 2)
}

func (s *TestFlingerSuite) TestDoGivesUpOnPersistent5xx(c *C) {
	attempts := 0
	p, cleanup := fakeTestFlinger(c, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/v1/result/job" {
			return false
		}
		attempts++
		w.WriteHeader(http.StatusServiceUnavailable)
		return true
	})
	defer cleanup()

	var result map[string]interface{}
	err := spread.DoTestFlingerRequest(p, "GET", "/result/job", &result)
	c.Assert(err, ErrorMatches, "cannot perform TestFlinger request: GET /v1/result/job failed after 4 attempts: HTTP status 503")
	c.Assert(attempts, Equals, 4)
}
