package enrollment

import (
	"context"
	"fmt"
	"net/http"

	"connectrpc.com/connect"

	appsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/apps/v1"
	"github.com/agynio/telegram-connector/.gen/go/agynio/api/gateway/v1/gatewayv1connect"
)

type EnrollResult struct {
	IdentityJSON []byte
	IdentityID   string
}

func Enroll(ctx context.Context, gatewayURL, serviceToken string) (EnrollResult, error) {
	client := gatewayv1connect.NewAppsGatewayClient(http.DefaultClient, gatewayURL)
	request := connect.NewRequest(&appsv1.EnrollAppRequest{
		ServiceToken: serviceToken,
	})
	response, err := client.EnrollApp(ctx, request)
	if err != nil {
		return EnrollResult{}, fmt.Errorf("enroll app: %w", err)
	}
	identityJSON := response.Msg.GetIdentityJson()
	identityID := response.Msg.GetIdentityId()
	if len(identityJSON) == 0 {
		return EnrollResult{}, fmt.Errorf("enroll app: empty identity json")
	}
	if identityID == "" {
		return EnrollResult{}, fmt.Errorf("enroll app: empty identity id")
	}
	return EnrollResult{IdentityJSON: identityJSON, IdentityID: identityID}, nil
}
