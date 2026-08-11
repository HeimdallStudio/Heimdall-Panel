package sub

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Identity/location tokens are part of each generated config's explicit name.
// They must be rendered on every subscription-body link. Their values must not
// be suppressed merely because another link for the same client already used
// them.
func TestRemarkTemplate_IdentityTokensRepeatOnEveryLink(t *testing.T) {
	s := &SubService{
		remarkTemplate:   "{{EMAIL}} | {{INBOUND}} | {{HOST}}",
		subscriptionBody: true,
		usageShown:       map[string]bool{},
	}

	client := model.Client{Email: "Mohammad"}

	cases := []struct {
		inbound string
		host    string
		want    string
	}{
		{"Germany", "Germany Host", "Mohammad | Germany | Germany Host"},
		{"Finland", "Finland Host", "Mohammad | Finland | Finland Host"},
		{"USA", "USA Host", "Mohammad | USA | USA Host"},
	}

	for i, tc := range cases {
		inbound := &model.Inbound{Remark: tc.inbound}

		got := s.genTemplatedRemark(
			inbound,
			client,
			tc.host,
			"",
		)

		if got != tc.want {
			t.Fatalf(
				"link %d remark = %q, want %q",
				i+1,
				got,
				tc.want,
			)
		}
	}
}

// Equal token values are still distinct template positions. Heimdall must not
// deduplicate EMAIL, INBOUND or HOST by value, nor suppress them on later links.
func TestRemarkTemplate_EqualIdentityValuesAreNeverDeduplicated(t *testing.T) {
	s := &SubService{
		remarkTemplate:   "{{EMAIL}} | {{INBOUND}} | {{HOST}}",
		subscriptionBody: true,
		usageShown:       map[string]bool{},
	}

	client := model.Client{Email: "same"}
	inbound := &model.Inbound{Remark: "same"}

	const want = "same | same | same"

	for i := 1; i <= 3; i++ {
		got := s.genTemplatedRemark(
			inbound,
			client,
			"same",
			"",
		)

		if got != want {
			t.Fatalf(
				"link %d equal-value remark = %q, want %q",
				i,
				got,
				want,
			)
		}
	}
}
