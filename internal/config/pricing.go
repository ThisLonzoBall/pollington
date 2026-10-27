package config

import "strings"

// Pricing is what a model costs, in US dollars per million tokens.
type Pricing struct {
	Input  float64
	Output float64
}

// Known reports whether there is a rate worth putting on screen.
func (p Pricing) Known() bool { return p.Input > 0 || p.Output > 0 }

// Cost estimates the dollars spent on a token count.
//
// It is an *upper* bound, for two reasons worth knowing. Cached input is billed
// far cheaper than fresh input - MiniMax charges $0.03/M against $0.255 - and a
// long session hits that cache on nearly every turn, so the real bill is lower
// than this. Meanwhile reasoning tokens are billed as output, and are already
// inside the count the provider reports, which is why a thinking model costs
// several times what the length of its answer suggests.
func (p Pricing) Cost(in, out int) float64 {
	return (float64(in)*p.Input + float64(out)*p.Output) / 1e6
}

// prices are keyed by a lowercase model-ID prefix, because providers append
// versions and dates to the IDs they publish: the MiniMax preset asks for
// "MiniMax-M2.7" and the rate is published against the M2 family.
//
// Deliberately short. Published rates move, often downward and sometimes
// overnight, and the ones here could not all be verified from a primary source
// - DeepSeek's current figure, for instance, is reported four different ways by
// four aggregators. A model that is absent shows no cost at all, which is the
// right answer: a wrong number on screen gets believed, and the whole point of
// showing cost is to be trusted about it. Set the rate for anything missing in
// config.json with "input_price" and "output_price".
var prices = map[string]Pricing{
	"minimax-m2":          {Input: 0.255, Output: 1.02},
	"openai/gpt-oss-120b": {Input: 0.15, Output: 0.60},
}

// PriceFor returns the published rate for a model, or a zero Pricing if it is
// not listed. Matching is case-insensitive and by longest prefix, so a more
// specific key always beats a more general one.
func PriceFor(model string) Pricing {
	name := strings.ToLower(model)
	var (
		best  string
		found Pricing
	)
	for prefix, p := range prices {
		if strings.HasPrefix(name, prefix) && len(prefix) > len(best) {
			best, found = prefix, p
		}
	}
	return found
}

// Price resolves the rate to charge this session at: an explicit override in
// the config file wins over the built-in table, so a stale figure here never
// has to be waited on.
func (c Config) Price() Pricing {
	if p := (Pricing{Input: c.InputPrice, Output: c.OutputPrice}); p.Known() {
		return p
	}
	return PriceFor(c.Model)
}
