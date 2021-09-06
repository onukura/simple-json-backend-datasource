package plugin

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bitly/go-simplejson"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/datasource"
	"github.com/grafana/grafana-plugin-sdk-go/backend/instancemgmt"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"golang.org/x/net/context/ctxhttp"
)

// Make sure SampleDatasource implements required interfaces. This is important to do
// since otherwise we will only get a not implemented error response from plugin in
// runtime. In this example datasource instance implements backend.QueryDataHandler,
// backend.CheckHealthHandler, backend.StreamHandler interfaces. Plugin should not
// implement all these interfaces - only those which are required for a particular task.
// For example if plugin does not need streaming functionality then you are free to remove
// methods that implement backend.StreamHandler. Implementing instancemgmt.InstanceDisposer
// is useful to clean up resources used by previous datasource instance when a new datasource
// instance created upon datasource settings changed.
var (
	_ backend.QueryDataHandler      = (*JsonDatasource)(nil)
	_ backend.CheckHealthHandler    = (*JsonDatasource)(nil)
	_ instancemgmt.InstanceDisposer = (*JsonDatasource)(nil)
)

// NewJsonDatasource creates a new datasource instance.
func NewJsonDatasource() *JsonDatasource {
	im := datasource.NewInstanceManager(newJsonDatasourceInstance)
	return &JsonDatasource{
		im: im,
	}
}

type JsonDatasourceInstance struct {
	dsInfo *backend.DataSourceInstanceSettings
}

func newJsonDatasourceInstance(settings backend.DataSourceInstanceSettings) (instancemgmt.Instance, error) {
	backend.Logger.Debug("Initializing new data source instance")

	return &JsonDatasourceInstance{
		dsInfo: &settings,
	}, nil
}

type JsonDatasource struct {
	im instancemgmt.InstanceManager
}

// Dispose here tells plugin SDK that plugin wants to clean up resources when a new instance
// created. As soon as datasource settings change detected by SDK old datasource instance will
// be disposed and a new one will be created using NewSampleDatasource factory function.
func (ds *JsonDatasource) Dispose() {
	// Clean up datasource instance resources.
}

func (ds *JsonDatasource) getInstance(ctx backend.PluginContext) (*dataSourceInstance, error) {
	instance, err := ds.im.Get(ctx)
	if err != nil {
		backend.Logger.Error(err.Error())
		return nil, err
	}
	return instance.(*dataSourceInstance), nil
}

// dataSourceInstance represents a single instance of this data source.
type dataSourceInstance struct {
	settings backend.DataSourceInstanceSettings
}

// CheckHealth handles health checks sent from Grafana to the plugin.
// The main use case for these health checks is the test button on the
// datasource configuration page which allows users to verify that
// a datasource is working as expected.
func (ds *JsonDatasource) CheckHealth(_ context.Context, req *backend.CheckHealthRequest) (*backend.CheckHealthResult, error) {
	res := &backend.CheckHealthResult{}

	_, err := ds.im.Get(req.PluginContext)
	if err != nil {
		res.Status = backend.HealthStatusError
		res.Message = "Error getting datasource instance"
		backend.Logger.Error("Error getting datasource instance", "err", err)
		return res, nil
	}

	res.Status = backend.HealthStatusOk
	res.Message = "plugin is running"
	return res, nil
}

// QueryData handles multiple queries and returns multiple responses.
// req contains the queries []DataQuery (where each query contains RefID as a unique identifier).
// The QueryDataResponse contains a map of RefID to the response for each query, and each response
// contains Frames ([]*Frame).
func (ds *JsonDatasource) QueryData(ctx context.Context, req *backend.QueryDataRequest) (*backend.QueryDataResponse, error) {
	// create response struct
	responses := backend.NewQueryDataResponse()

	backend.Logger.Info("ok 1")
	instance, err := ds.getInstance(req.PluginContext)
	if err != nil {
		backend.Logger.Error(err.Error())
		return nil, err
	}

	backend.Logger.Info("QueryData called", "request", req)

	var wg sync.WaitGroup
	wg.Add(len(req.Queries))
	backend.Logger.Info("ok 3")

	// loop over queries and execute them individually.
	for _, q := range req.Queries {
		go func(q backend.DataQuery) {
			backend.Logger.Info("ok 4")
			res := ds.query(ctx, q, instance)
			responses.Responses[q.RefID] = res
			wg.Done()
		}(q)
	}

	// Wait for all queries to finish before returning the result.
	wg.Wait()

	return responses, nil
}

func (ds *JsonDatasource) query(ctx context.Context, query backend.DataQuery, instance *dataSourceInstance) backend.DataResponse {
	backend.Logger.Info("ok query 1")
	remoteDsReq, err := ds.createMetricRequest(&query, instance)
	if err != nil {
		return backend.DataResponse{Error: err}
	}

	backend.Logger.Info("ok query 2")
	body, err := ds.makeHttpRequest(ctx, remoteDsReq)
	if err != nil {
		return backend.DataResponse{Error: err}
	}

	backend.Logger.Info("ok query 3")
	res := ds.parseQueryResponse(&query, body)
	if res.Error != nil {
		return backend.DataResponse{Error: err}
	}

	backend.Logger.Info("ok query 4")
	return res
}

func (ds *JsonDatasource) createMetricRequest(q *backend.DataQuery, instance *dataSourceInstance) (*RemoteDatasourceRequest, error) {
	jsonQueries, err := simplejson.NewJson(q.JSON)
	if err != nil {
		return nil, err
	}
	payload := simplejson.New()
	payload.SetPath([]string{"range", "to"}, q.TimeRange.From.String())
	payload.SetPath([]string{"range", "from"}, q.TimeRange.To.String())
	payload.Set("targets", jsonQueries)

	body, err := payload.MarshalJSON()
	if err != nil {
		return nil, err
	}

	url := instance.settings.URL + "/query"
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}

	req.Header.Add("Content-Type", "application/json")

	return &RemoteDatasourceRequest{
		queryType: "query",
		req:       req,
		queries:   jsonQueries,
	}, nil
}

func (ds *JsonDatasource) makeHttpRequest(ctx context.Context, remoteDsReq *RemoteDatasourceRequest) ([]byte, error) {
	res, err := ctxhttp.Do(ctx, httpClient, remoteDsReq.req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		msg := fmt.Errorf("invalid status code. status: %v", res.Status)
		backend.Logger.Error(msg.Error())
		return nil, msg
	}

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func (ds *JsonDatasource) parseQueryResponse(query *backend.DataQuery, body []byte) backend.DataResponse {
	//refId := query.RefID
	response := backend.DataResponse{}

	var responseBody []TargetResponseDTO

	// Unmarshal the JSON into our TargetResponseDTO.
	response.Error = json.Unmarshal(body, &responseBody)
	if response.Error != nil {
		return response
	}

	// create data frame response.
	frame := data.NewFrame("response")

	// add fields.
	// test
	frame.Fields = append(frame.Fields,
		data.NewField("time", nil, []time.Time{query.TimeRange.From, query.TimeRange.To}),
		data.NewField("values", nil, []int64{10, 20}),
	)
	//
	//for i, r := range responseBody {
	//
	//	field := *data.Field
	//	qr := datasource.QueryResult{
	//		RefId:  refId,
	//		Series: make([]*datasource.TimeSeries, 0),
	//		Tables: make([]*datasource.Table, 0),
	//	}
	//
	//	if len(r.Columns) > 0 {
	//		table := datasource.Table{
	//			Columns: make([]*datasource.TableColumn, 0),
	//			Rows:    make([]*datasource.TableRow, 0),
	//		}
	//
	//		for _, c := range r.Columns {
	//			table.Columns = append(table.Columns, &datasource.TableColumn{
	//				Name: c.Text,
	//			})
	//		}
	//
	//		for _, row := range r.Rows {
	//			values := make([]*datasource.RowValue, 0)
	//
	//			for i, cell := range row {
	//				rv := datasource.RowValue{}
	//
	//				switch r.Columns[i].Type {
	//				case "time":
	//					if timeValue, ok := cell.(float64); ok {
	//						rv.Int64Value = int64(timeValue)
	//					}
	//					rv.Kind = datasource.RowValue_TYPE_INT64
	//				case "number":
	//					if numberValue, ok := cell.(float64); ok {
	//						rv.Int64Value = int64(numberValue)
	//					}
	//					rv.Kind = datasource.RowValue_TYPE_INT64
	//				case "string":
	//					if stringValue, ok := cell.(string); ok {
	//						rv.StringValue = stringValue
	//					}
	//					rv.Kind = datasource.RowValue_TYPE_STRING
	//				default:
	//					ds.logger.Debug(fmt.Sprintf("failed to parse value %v of type %T", cell, cell))
	//				}
	//
	//				values = append(values, &rv)
	//			}
	//
	//			table.Rows = append(table.Rows, &datasource.TableRow{Values: values})
	//		}
	//		field
	//		qr.Tables = append(qr.Tables, &table)
	//	} else {
	//		serie := &datasource.TimeSeries{Name: r.Target}
	//
	//		for _, p := range r.DataPoints {
	//			serie.Points = append(serie.Points, &datasource.Point{
	//				Timestamp: int64(p[1]),
	//				Value:     p[0],
	//			})
	//		}
	//
	//		qr.Series = append(qr.Series, serie)
	//	}
	//
	//	response.Responses[refId] = qr
	//	//response.Results = append(response.Results, &qr)
	//}

	// add the frames to the response.
	response.Frames = append(response.Frames, frame)

	return response
}

var httpClient = &http.Client{
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			Renegotiation: tls.RenegotiateFreelyAsClient,
		},
		Proxy:                 http.ProxyFromEnvironment,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
	},
	Timeout: time.Second * 30,
}
