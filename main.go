package main

import (
	"crypto/tls"
	"fmt"
	"os"

	"net/http"

	"strings"

	"html/template"

	"github.com/kicombs/componentMetrics/Godeps/_workspace/src/github.com/cloudfoundry/noaa"
	"github.com/kicombs/componentMetrics/Godeps/_workspace/src/github.com/cloudfoundry/sonde-go/events"
)

// FUTURE if time, removing metrics after TTL?

var dopplerAddress = os.Getenv("DOPPLER_ADDR") // should look like ws://host:port
var authToken = os.Getenv("CF_ACCESS_TOKEN")   // use $(cf oauth-token | grep bearer)

const firehoseSubscriptionId = "firehose-a"

type metricCategory interface {
	GetCategory() string
}

// IMPORTANT!!!!
// In order for Json to marshal the members must be public (aka capitalized)
type metricCategoryOnly struct {
	Category string
}

func (m metricCategoryOnly) GetCategory() string {
	return m.Category
}

// IMPORTANT!!!!
// In order for Json to marshal the members must be public (aka capitalized)
type metricCategoryWithSubCategory struct {
	Category    string
	SubCategory []string
}

func (m metricCategoryWithSubCategory) GetCategory() string {
	return m.Category
}

func main() {
	var messages map[string][]metricCategory // [origin]{metricCategory To []names}]
	messages = make(map[string][]metricCategory)
	connection := noaa.NewConsumer(dopplerAddress, &tls.Config{InsecureSkipVerify: true}, nil)
	connection.SetDebugPrinter(ConsoleDebugPrinter{})

	fmt.Println("===== Streaming Firehose (will only succeed if you have admin credentials)")

	msgChan := make(chan *events.Envelope)
	go func() {
		defer close(msgChan)
		errorChan := make(chan error)
		go connection.Firehose(firehoseSubscriptionId, authToken, msgChan, errorChan)

		for err := range errorChan {
			fmt.Fprintf(os.Stderr, "%v\n", err.Error())
		}
	}()

	go startHttp(messages)

	for msg := range msgChan {
		vm := msg.GetValueMetric()
		if vm == nil {
			continue
		}
		origin := msg.GetOrigin()

		category, subCategory := parseMetric(*vm.Name)

		index := indexOf(messages[origin], category)
		if index >= 0 {
			metricCategoryGroup := messages[origin][index]
			switch f := metricCategoryGroup.(type) {
			case *metricCategoryWithSubCategory:
				if len(subCategory) > 0 {
					if contains(f.SubCategory, subCategory) == false {
						f.SubCategory = append(f.SubCategory, subCategory)
					}
				}
			default:
			}
		} else {
			if len(subCategory) > 0 {
				messages[origin] = append(messages[origin], &metricCategoryWithSubCategory{
					Category:    category,
					SubCategory: []string{subCategory},
				})
			} else {
				messages[origin] = append(messages[origin], &metricCategoryOnly{
					Category: category,
				})
			}
		}
	}
}

func parseMetric(metric string) (category, subCategory string) {
	//TODO logSenderTotalMessagesRead.{appId} ignore
	separatedMetric := strings.SplitN(metric, ".", 2)
	if len(separatedMetric) < 2 {
		return metric, ""
	}
	return separatedMetric[0], separatedMetric[1]
}

func indexOf(metricsCollected []metricCategory, key string) int {
	for index, entry := range metricsCollected {
		metricCategoryVar := entry
		if metricCategoryVar.GetCategory() == key {
			return index
		}
	}
	return -1
}

func contains(slice []string, value string) bool {
	for _, entry := range slice {
		if entry == value {
			return true
		}
	}
	return false

}

type ConsoleDebugPrinter struct{}

func (c ConsoleDebugPrinter) Print(title, dump string) {
	println(title)
	println(dump)
}

func startHttp(messages map[string][]metricCategory) {
	http.Handle("/messages", NewMetricsListingHandler(messages))

	port := os.Getenv("PORT")
	err := http.ListenAndServe(fmt.Sprintf(":%v", port), nil)
	if err != nil {
		println("Proxy Server Error", err)
		panic("We could not start the HTTP listener")
	}
}

func NewMetricsListingHandler(instanceToMetrics map[string][]metricCategory) http.Handler {
	return metricsListingHandler{instancesToMetrics: instanceToMetrics}
}

type metricsListingHandler struct {
	instancesToMetrics map[string][]metricCategory
}

func (m metricsListingHandler) ServeHTTP(res http.ResponseWriter, req *http.Request) {
	res.Header().Set("Content-Type", "html")

	m.render(res)
}

func (m metricsListingHandler) render(res http.ResponseWriter) {
	printCategory := func(typeInterface metricCategory) template.HTML {
		td := ""
		switch f := typeInterface.(type) {
		case *metricCategoryWithSubCategory:
			td = fmt.Sprintf("<td>%s</td>", f.Category)
			break
		case *metricCategoryOnly:
			td = fmt.Sprintf("<td style=\"border-right:none\">%s</td>", f.Category)
			break
		default:
		}
		return template.HTML(td)
	}

	printSubCategory := func(typeInterface metricCategory) template.HTML {
		td := ""
		switch f := typeInterface.(type) {
		case *metricCategoryWithSubCategory:
			t := "<td><table border=0>"
			for _, subCat := range f.SubCategory {
				base, sub := parseMetric(subCat)
				t += fmt.Sprintf("<tr><td>%s", base)
				for len(sub) > 0 {
					t += fmt.Sprintf("<table border =0><tr><td>%s</td></tr>", sub)
					base, sub = parseMetric(subCat)
				}
				t += fmt.Sprintf("</td></tr>")
			}
			td = t + "</table></td>"
			break
		case *metricCategoryOnly:
			td = "<td style=\"border-left:none\"></td>"
			break
		default:
		}
		return template.HTML(td)
	}

	buildOriginRowSpan := func(indexOfSlice int, origin string, categories []metricCategory) template.HTML {
		td := ""
		if indexOfSlice == 0 {
			if len(categories) > 1 {
				td = fmt.Sprintf("<td rowspan=%d>%s</td>", len(categories), origin)
			} else {
				td = fmt.Sprintf("<td>%s</td>", origin)
			}
		}
		return template.HTML(td)
	}

	totalMetrics := func(metrics map[string][]metricCategory) int {
		total := 0
		for _, origin := range metrics {
			for _, categoryGroup := range origin {
				switch f := categoryGroup.(type) {
				case *metricCategoryWithSubCategory:
					total += len(f.SubCategory)
					break
				case *metricCategoryOnly:
					total++
					break
				default:
				}
			}

		}
		return total
	}

	htmlTemplate := `<!DOCTYPE html>
<html>
<head></head>
<body>
<h1>Loggregator Metrics</h1>
<p>Total Number of Metrics: {{ totalMetrics . }}</p>
<table border=1><tr><th>Origin</th><th>Category</th><th>Sub Category</th></tr>
{{range $index, $item := .}}
{{range $counter, $element := $item}}
<tr>
{{ buildOriginRowSpan $counter $index $item  }}
{{printCategory $element}}
{{printSubCategory $element}}</tr>
{{end}}
{{end}}</table></body>
</html>`

	t := template.New("t").Funcs(template.FuncMap{"printCategory": printCategory,
		"printSubCategory":   printSubCategory,
		"buildOriginRowSpan": buildOriginRowSpan,
		"totalMetrics":       totalMetrics})

	t, err := t.Parse(htmlTemplate)
	if err != nil {
		fmt.Println("ERROR: could not parse template: ", err.Error())
		return
	}

	err = t.Execute(res, m.instancesToMetrics)
	if err != nil {
		panic(err)
	}
}
