/**
* OPA Lambda
*
* MIT License. See LICENSE file for details.
 */
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"opa_lambda/policyevaluator"
	"opa_lambda/policyloader"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	log "github.com/sirupsen/logrus"
)

// A LambdaRequest is the event used to invoke the Lambda function.
type LambdaEvent struct {
	PolicyName string           `json:"policy"`  // The name of the OPA policy to check.
	Payload    *json.RawMessage `json:"payload"` // The payload to evaluate the policy against.
}

type LambdaResponse struct {
	Output interface{} `json:"output,omitempty"` // The output of the policy evaluation.
	Error  string      `json:"error,omitempty"`  // The error, if any, that occurred during policy evaluation.
}

// Handle requests for policy evaluation when running on AWS Lambda.
func handleLambda(ctx context.Context, payload json.RawMessage) (interface{}, error) {
	log.SetFormatter(&log.JSONFormatter{})

	if isALBEvent(payload) {
		return handleALBRequest(ctx, payload)
	}
	if isAPIGatewayV2Event(payload) {
		return handleAPIGatewayV2Request(ctx, payload)
	}
	if isAPIGatewayProxyEvent(payload) {
		return handleAPIGatewayProxyRequest(ctx, payload)
	}

	return handleDirectLambdaEvent(ctx, payload)
}

func handleDirectLambdaEvent(ctx context.Context, payload json.RawMessage) (LambdaResponse, error) {
	var req LambdaEvent
	if err := json.Unmarshal(payload, &req); err != nil {
		err = fmt.Errorf("unable to parse lambda payload: %w", err)
		log.Error(err)
		return LambdaResponse{Error: err.Error()}, err
	}

	value, err := evaluatePolicy(ctx, req)
	if err != nil {
		log.Error(err)
		return LambdaResponse{Error: err.Error()}, err
	}

	return LambdaResponse{Output: value}, nil
}

func handleALBRequest(ctx context.Context, payload json.RawMessage) (events.ALBTargetGroupResponse, error) {
	var req events.ALBTargetGroupRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		err = fmt.Errorf("unable to parse ALB payload: %w", err)
		log.Error(err)
		return newALBErrorResponse(http.StatusBadRequest, err), nil
	}

	body, err := decodeBody(req.Body, req.IsBase64Encoded)
	if err != nil {
		log.Error(err)
		return newALBErrorResponse(http.StatusBadRequest, err), nil
	}

	var lambdaReq LambdaEvent
	if err := json.Unmarshal(body, &lambdaReq); err != nil {
		err = fmt.Errorf("unable to parse ALB body: %w", err)
		log.Error(err)
		return newALBErrorResponse(http.StatusBadRequest, err), nil
	}

	value, err := evaluatePolicy(ctx, lambdaReq)
	if err != nil {
		log.Error(err)
		return newALBErrorResponse(http.StatusInternalServerError, err), nil
	}

	return newALBResponse(http.StatusOK, LambdaResponse{Output: value}), nil
}

func handleAPIGatewayProxyRequest(ctx context.Context, payload json.RawMessage) (events.APIGatewayProxyResponse, error) {
	var req events.APIGatewayProxyRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		err = fmt.Errorf("unable to parse API Gateway proxy payload: %w", err)
		log.Error(err)
		return newAPIGatewayProxyErrorResponse(http.StatusBadRequest, err), nil
	}

	body, err := decodeBody(req.Body, req.IsBase64Encoded)
	if err != nil {
		log.Error(err)
		return newAPIGatewayProxyErrorResponse(http.StatusBadRequest, err), nil
	}

	var lambdaReq LambdaEvent
	if err := json.Unmarshal(body, &lambdaReq); err != nil {
		err = fmt.Errorf("unable to parse API Gateway body: %w", err)
		log.Error(err)
		return newAPIGatewayProxyErrorResponse(http.StatusBadRequest, err), nil
	}

	value, err := evaluatePolicy(ctx, lambdaReq)
	if err != nil {
		log.Error(err)
		return newAPIGatewayProxyErrorResponse(http.StatusInternalServerError, err), nil
	}

	return newAPIGatewayProxyResponse(http.StatusOK, LambdaResponse{Output: value}), nil
}

func handleAPIGatewayV2Request(ctx context.Context, payload json.RawMessage) (events.APIGatewayV2HTTPResponse, error) {
	var req events.APIGatewayV2HTTPRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		err = fmt.Errorf("unable to parse API Gateway v2 payload: %w", err)
		log.Error(err)
		return newAPIGatewayV2ErrorResponse(http.StatusBadRequest, err), nil
	}

	body, err := decodeBody(req.Body, req.IsBase64Encoded)
	if err != nil {
		log.Error(err)
		return newAPIGatewayV2ErrorResponse(http.StatusBadRequest, err), nil
	}

	var lambdaReq LambdaEvent
	if err := json.Unmarshal(body, &lambdaReq); err != nil {
		err = fmt.Errorf("unable to parse API Gateway v2 body: %w", err)
		log.Error(err)
		return newAPIGatewayV2ErrorResponse(http.StatusBadRequest, err), nil
	}

	value, err := evaluatePolicy(ctx, lambdaReq)
	if err != nil {
		log.Error(err)
		return newAPIGatewayV2ErrorResponse(http.StatusInternalServerError, err), nil
	}

	return newAPIGatewayV2Response(http.StatusOK, LambdaResponse{Output: value}), nil
}

func decodeBody(body string, isBase64Encoded bool) ([]byte, error) {
	if body == "" {
		return nil, errors.New("request body is required")
	}

	if isBase64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return nil, fmt.Errorf("invalid base64 body: %w", err)
		}
		return decoded, nil
	}

	return []byte(body), nil
}

func newALBErrorResponse(status int, err error) events.ALBTargetGroupResponse {
	return newALBResponse(status, LambdaResponse{Error: err.Error()})
}

func newALBResponse(status int, body LambdaResponse) events.ALBTargetGroupResponse {
	payload, err := json.Marshal(body)
	if err != nil {
		log.Errorf("unable to marshal ALB response: %v", err)
		status = http.StatusInternalServerError
		payload = []byte(fmt.Sprintf(`{"error":"%s"}`, http.StatusText(status)))
	}

	return events.ALBTargetGroupResponse{
		StatusCode:        status,
		StatusDescription: fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
		Body:            string(payload),
		IsBase64Encoded: false,
	}
}

func newAPIGatewayProxyErrorResponse(status int, err error) events.APIGatewayProxyResponse {
	return newAPIGatewayProxyResponse(status, LambdaResponse{Error: err.Error()})
}

func newAPIGatewayProxyResponse(status int, body LambdaResponse) events.APIGatewayProxyResponse {
	payload, err := json.Marshal(body)
	if err != nil {
		log.Errorf("unable to marshal API Gateway response: %v", err)
		status = http.StatusInternalServerError
		payload = []byte(fmt.Sprintf(`{"error":"%s"}`, http.StatusText(status)))
	}

	return events.APIGatewayProxyResponse{
		StatusCode:      status,
		Headers:         map[string]string{"Content-Type": "application/json"},
		Body:            string(payload),
		IsBase64Encoded: false,
	}
}

func newAPIGatewayV2ErrorResponse(status int, err error) events.APIGatewayV2HTTPResponse {
	return newAPIGatewayV2Response(status, LambdaResponse{Error: err.Error()})
}

func newAPIGatewayV2Response(status int, body LambdaResponse) events.APIGatewayV2HTTPResponse {
	payload, err := json.Marshal(body)
	if err != nil {
		log.Errorf("unable to marshal API Gateway v2 response: %v", err)
		status = http.StatusInternalServerError
		payload = []byte(fmt.Sprintf(`{"error":"%s"}`, http.StatusText(status)))
	}

	return events.APIGatewayV2HTTPResponse{
		StatusCode:      status,
		Headers:         map[string]string{"Content-Type": "application/json"},
		Body:            string(payload),
		IsBase64Encoded: false,
	}
}

func evaluatePolicy(ctx context.Context, req LambdaEvent) (interface{}, error) {
	if req.PolicyName == "" {
		return nil, errors.New("policy is required")
	}
	if req.Payload == nil {
		return nil, errors.New("payload is required")
	}

	log.Infof("Evaluating policy: %s", req.PolicyName)

	pl, err := policyloader.NewPolicyLoader(ctx)
	if err != nil {
		return nil, err
	}

	pe := policyevaluator.NewPolicyEvaluator(pl)
	result, err := pe.EvaluatePolicy(ctx, req.PolicyName, *req.Payload)
	if err != nil {
		return nil, err
	}

	return result.Value, nil
}

func isALBEvent(payload json.RawMessage) bool {
	var probe struct {
		RequestContext struct {
			Elb struct {
				TargetGroupArn string `json:"targetGroupArn"`
			} `json:"elb"`
		} `json:"requestContext"`
	}

	if err := json.Unmarshal(payload, &probe); err != nil {
		return false
	}

	return probe.RequestContext.Elb.TargetGroupArn != ""
}

func isAPIGatewayProxyEvent(payload json.RawMessage) bool {
	var probe struct {
		Resource       string `json:"resource"`
		Version        string `json:"version"`
		RequestContext struct {
			ApiID string `json:"apiId"`
			Stage string `json:"stage"`
		} `json:"requestContext"`
	}

	if err := json.Unmarshal(payload, &probe); err != nil {
		return false
	}

	if probe.Version == "2.0" {
		return false
	}

	return probe.Resource != "" || probe.RequestContext.ApiID != "" || probe.RequestContext.Stage != ""
}

func isAPIGatewayV2Event(payload json.RawMessage) bool {
	var probe struct {
		Version        string `json:"version"`
		RawPath        string `json:"rawPath"`
		RequestContext struct {
			ApiID string `json:"apiId"`
		} `json:"requestContext"`
	}

	if err := json.Unmarshal(payload, &probe); err != nil {
		return false
	}

	return probe.Version == "2.0" || probe.RawPath != "" || probe.RequestContext.ApiID != ""
}

// Handle requests when testing locally.
func handleLocal() {
	log.SetFormatter(&log.TextFormatter{})

	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		log.Fatal("Unable to read input from stdin")
	}

	ctx := context.Background()
	payload := json.RawMessage(input)
	req := LambdaEvent{PolicyName: os.Args[1], Payload: &payload}

	value, err := evaluatePolicy(ctx, req)
	if err != nil {
		log.Fatal(err)
	}

	output, err := json.Marshal(value)
	if err != nil {
		log.Fatal(err)
	}

	log.Println(string(output))
}

func main() {
	if os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != "" {
		// Lambda Environment
		lambda.Start(handleLambda)
	} else {
		// Local development
		handleLocal()
	}
}
