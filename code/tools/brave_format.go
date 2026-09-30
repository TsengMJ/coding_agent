package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// braveResponse is the subset of the Brave web search response rendered for
// the model.
type braveResponse struct {
	Query *struct {
		Original             string `json:"original"`
		Altered              string `json:"altered"`
		MoreResultsAvailable bool   `json:"more_results_available"`
	} `json:"query"`
	Web         *braveResults[braveWebResult]   `json:"web"`
	News        *braveResults[braveNewsResult]  `json:"news"`
	Videos      *braveResults[braveVideoResult] `json:"videos"`
	Discussions *braveResults[braveDiscussion]  `json:"discussions"`
	FAQ         *braveResults[braveFAQ]         `json:"faq"`
	Infobox     *braveResults[braveInfobox]     `json:"infobox"`
	Locations   *braveResults[braveLocation]    `json:"locations"`
	Summarizer  *struct{ Key string }           `json:"summarizer"`
	Rich        *struct{ Hint braveRichHint }   `json:"rich"`
}

type braveResults[T any] struct {
	Results []T `json:"results"`
}

type braveRichHint struct {
	Vertical    string `json:"vertical"`
	CallbackKey string `json:"callback_key"`
}

type braveWebResult struct {
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	Description   string   `json:"description"`
	Age           string   `json:"age"`
	ExtraSnippets []string `json:"extra_snippets"`
	FetchedAt     any      `json:"fetched_content_timestamp"`
}

type braveNewsResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Age         string `json:"age"`
	MetaURL     struct {
		Hostname string `json:"hostname"`
	} `json:"meta_url"`
}

type braveVideoResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Age         string `json:"age"`
	Video       struct {
		Duration  string `json:"duration"`
		Creator   string `json:"creator"`
		Publisher string `json:"publisher"`
	} `json:"video"`
}

type braveDiscussion struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Data        struct {
		ForumName  string `json:"forum_name"`
		NumAnswers int    `json:"num_answers"`
		Question   string `json:"question"`
		TopComment string `json:"top_comment"`
	} `json:"data"`
}

type braveFAQ struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
	Title    string `json:"title"`
	URL      string `json:"url"`
}

type braveInfobox struct {
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Description string  `json:"description"`
	LongDesc    string  `json:"long_desc"`
	Attributes  [][]any `json:"attributes"`
}

type braveLocation struct {
	Title         string `json:"title"`
	URL           string `json:"url"`
	PostalAddress struct {
		DisplayAddress string `json:"displayAddress"`
	} `json:"postal_address"`
	Rating *struct {
		RatingValue float64 `json:"ratingValue"`
		ReviewCount int     `json:"reviewCount"`
	} `json:"rating"`
	Contact struct {
		Telephone string `json:"telephone"`
	} `json:"contact"`
	Coordinates []float64 `json:"coordinates"`
}

type braveSummary struct {
	Status  string `json:"status"`
	Title   string `json:"title"`
	Summary []struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	} `json:"summary"`
	Enrichments struct {
		Raw     string `json:"raw"`
		Sources []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"sources"`
	} `json:"enrichments"`
	Followups []string `json:"followups"`
}

// tokens joins the summary's text tokens, rendering inline references as
// " [url]".
func (s braveSummary) tokens() string {
	var b strings.Builder
	for _, part := range s.Summary {
		switch part.Type {
		case "token":
			var t string
			if json.Unmarshal(part.Data, &t) == nil {
				b.WriteString(t)
			}
		case "inline_reference":
			var ref struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(part.Data, &ref) == nil && ref.URL != "" {
				b.WriteString(" [" + ref.URL + "]")
			}
		}
	}
	return b.String()
}

func (r *braveResponse) writeQuery(out *strings.Builder) {
	if r.Query == nil {
		return
	}
	fmt.Fprintf(out, "Query: %s\n", r.Query.Original)
	if r.Query.Altered != "" && r.Query.Altered != r.Query.Original {
		fmt.Fprintf(out, "Searched instead for: %s\n", r.Query.Altered)
	}
	if r.Query.MoreResultsAvailable {
		out.WriteString("More results available: raise offset to see the next page.\n")
	}
	out.WriteString("\n")
}

func (r *braveResponse) writeResults(out *strings.Builder) {
	if r.Web != nil && len(r.Web.Results) > 0 {
		out.WriteString("## Web results\n")
		for i, w := range r.Web.Results {
			fmt.Fprintf(out, "%d. %s\n   %s\n", i+1, w.Title, w.URL)
			line(out, w.Description)
			if w.Age != "" {
				line(out, "Age: "+w.Age)
			}
			if w.FetchedAt != nil {
				line(out, fmt.Sprintf("Fetched: %v", w.FetchedAt))
			}
			for _, s := range w.ExtraSnippets {
				line(out, "- "+s)
			}
		}
		out.WriteString("\n")
	}
	if r.News != nil && len(r.News.Results) > 0 {
		out.WriteString("## News\n")
		for i, n := range r.News.Results {
			fmt.Fprintf(out, "%d. %s\n   %s\n", i+1, n.Title, n.URL)
			line(out, joinNonEmpty(" · ", n.MetaURL.Hostname, n.Age))
			line(out, n.Description)
		}
		out.WriteString("\n")
	}
	if r.Videos != nil && len(r.Videos.Results) > 0 {
		out.WriteString("## Videos\n")
		for i, v := range r.Videos.Results {
			fmt.Fprintf(out, "%d. %s\n   %s\n", i+1, v.Title, v.URL)
			line(out, joinNonEmpty(" · ", v.Video.Creator, v.Video.Publisher, v.Video.Duration, v.Age))
			line(out, v.Description)
		}
		out.WriteString("\n")
	}
	if r.Discussions != nil && len(r.Discussions.Results) > 0 {
		out.WriteString("## Discussions\n")
		for i, d := range r.Discussions.Results {
			title := d.Title
			if d.Data.Question != "" {
				title = d.Data.Question
			}
			fmt.Fprintf(out, "%d. %s\n   %s\n", i+1, title, d.URL)
			meta := d.Data.ForumName
			if d.Data.NumAnswers > 0 {
				meta = joinNonEmpty(" · ", meta, fmt.Sprintf("%d answers", d.Data.NumAnswers))
			}
			line(out, meta)
			if d.Data.TopComment != "" {
				line(out, "Top comment: "+d.Data.TopComment)
			} else {
				line(out, d.Description)
			}
		}
		out.WriteString("\n")
	}
	if r.FAQ != nil && len(r.FAQ.Results) > 0 {
		out.WriteString("## FAQ\n")
		for _, f := range r.FAQ.Results {
			fmt.Fprintf(out, "Q: %s\nA: %s\n", f.Question, f.Answer)
			line(out, joinNonEmpty(" ", f.Title, f.URL))
		}
		out.WriteString("\n")
	}
	if r.Infobox != nil && len(r.Infobox.Results) > 0 {
		out.WriteString("## Infobox\n")
		for _, ib := range r.Infobox.Results {
			fmt.Fprintf(out, "%s\n", joinNonEmpty(" ", ib.Title, ib.URL))
			desc := ib.LongDesc
			if desc == "" {
				desc = ib.Description
			}
			line(out, desc)
			for _, attr := range ib.Attributes {
				if len(attr) == 2 && attr[0] != nil && attr[1] != nil {
					line(out, fmt.Sprintf("%v: %v", attr[0], attr[1]))
				}
			}
		}
		out.WriteString("\n")
	}
	if r.Locations != nil && len(r.Locations.Results) > 0 {
		out.WriteString("## Locations\n")
		for i, l := range r.Locations.Results {
			fmt.Fprintf(out, "%d. %s\n", i+1, l.Title)
			line(out, l.PostalAddress.DisplayAddress)
			if l.Rating != nil {
				line(out, fmt.Sprintf("Rating: %.1f (%d reviews)", l.Rating.RatingValue, l.Rating.ReviewCount))
			}
			if l.Contact.Telephone != "" {
				line(out, "Phone: "+l.Contact.Telephone)
			}
			if len(l.Coordinates) == 2 {
				line(out, fmt.Sprintf("Coordinates: %v, %v", l.Coordinates[0], l.Coordinates[1]))
			}
			line(out, l.URL)
		}
		out.WriteString("\n")
	}
}

// line writes an indented detail line when s is non-empty.
func line(out *strings.Builder, s string) {
	if s = strings.TrimSpace(s); s != "" {
		out.WriteString("   " + s + "\n")
	}
}

func joinNonEmpty(sep string, parts ...string) string {
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, sep)
}
