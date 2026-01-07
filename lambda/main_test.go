package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/require"
)

func TestHandleLambdaDirectEvent(t *testing.T) {
	ctx := context.Background()
	raw := buildLambdaEventPayload(t)

	resp, err := handleLambda(ctx, raw)
	require.NoError(t, err)

	lambdaResp, ok := resp.(LambdaResponse)
	require.True(t, ok)
	require.Empty(t, lambdaResp.Error)
	assertExampleOutput(t, lambdaResp.Output)
}

func TestHandleLambdaALBEvent(t *testing.T) {
	ctx := context.Background()
	body := string(buildLambdaEventPayloadBytes(t))

	event := events.ALBTargetGroupRequest{
		RequestContext: events.ALBTargetGroupRequestContext{
			ELB: events.ELBContext{TargetGroupArn: "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/opa/test"},
		},
		Body: body,
	}
	raw, err := json.Marshal(event)
	require.NoError(t, err)

	resp, err := handleLambda(ctx, raw)
	require.NoError(t, err)

	albResp, ok := resp.(events.ALBTargetGroupResponse)
	require.True(t, ok)
	require.Equal(t, http.StatusOK, albResp.StatusCode)

	lr := parseLambdaResponseBody(t, albResp.Body)
	require.Empty(t, lr.Error)
	assertExampleOutput(t, lr.Output)
}

func TestHandleLambdaAPIGatewayProxyEvent(t *testing.T) {
	ctx := context.Background()
	body := string(buildLambdaEventPayloadBytes(t))

	event := events.APIGatewayProxyRequest{
		Resource: "/opa",
		Path:     "/opa",
		Body:     body,
		RequestContext: events.APIGatewayProxyRequestContext{
			Stage: "dev",
			APIID: "abc123",
		},
	}

	raw, err := json.Marshal(event)
	require.NoError(t, err)

	resp, err := handleLambda(ctx, raw)
	require.NoError(t, err)

	gwResp, ok := resp.(events.APIGatewayProxyResponse)
	require.True(t, ok)
	require.Equal(t, http.StatusOK, gwResp.StatusCode)

	lr := parseLambdaResponseBody(t, gwResp.Body)
	require.Empty(t, lr.Error)
	assertExampleOutput(t, lr.Output)
}

func TestHandleLambdaAPIGatewayV2EventBase64(t *testing.T) {
	ctx := context.Background()
	body := base64.StdEncoding.EncodeToString(buildLambdaEventPayloadBytes(t))

	event := events.APIGatewayV2HTTPRequest{
		Version:         "2.0",
		RawPath:         "/opa",
		Body:            body,
		IsBase64Encoded: true,
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			APIID: "def456",
		},
	}

	raw, err := json.Marshal(event)
	require.NoError(t, err)

	resp, err := handleLambda(ctx, raw)
	require.NoError(t, err)

	gwResp, ok := resp.(events.APIGatewayV2HTTPResponse)
	require.True(t, ok)
	require.Equal(t, http.StatusOK, gwResp.StatusCode)

	lr := parseLambdaResponseBody(t, gwResp.Body)
	require.Empty(t, lr.Error)
	assertExampleOutput(t, lr.Output)
}

func buildLambdaEventPayload(t *testing.T) json.RawMessage {
	t.Helper()
	body := buildLambdaEventPayloadBytes(t)
	return json.RawMessage(body)
}

func buildLambdaEventPayloadBytes(t *testing.T) []byte {
	t.Helper()
	payload := map[string]interface{}{
		"policy": "example",
		"payload": map[string]interface{}{
			"membership": map[string]interface{}{
				"user": map[string]interface{}{
					"login": "jane",
					"mail":  "jane@example.com",
				},
			},
		},
	}

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	return raw
}

func parseLambdaResponseBody(t *testing.T, body string) LambdaResponse {
	t.Helper()
	var resp LambdaResponse
	require.NoError(t, json.Unmarshal([]byte(body), &resp))
	return resp
}

func assertExampleOutput(t *testing.T, output interface{}) {
	t.Helper()
	result, ok := output.(map[string]interface{})
	require.True(t, ok)

	require.Equal(t, true, result["allow"])
	require.Equal(t, "jane", result["user"])
	require.Equal(t, "jane@example.com", result["email"])
}
