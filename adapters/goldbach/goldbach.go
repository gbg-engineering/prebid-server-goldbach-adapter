package goldbach

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/prebid/openrtb/v20/openrtb2"
	"github.com/prebid/prebid-server/v3/adapters"
	"github.com/prebid/prebid-server/v3/config"
	"github.com/prebid/prebid-server/v3/errortypes"
	"github.com/prebid/prebid-server/v3/openrtb_ext"
	"github.com/prebid/prebid-server/v3/util/jsonutil"
)

type adapter struct {
	endpoint string
}

type impExtAdapter struct {
	Goldbach openrtb_ext.ImpExtGoldbach `json:"goldbach"`
}

func Builder(bidderName openrtb_ext.BidderName, config config.Adapter, server config.Server) (adapters.Bidder, error) {
	if config.Endpoint == "" {
		return nil, errors.New("missing endpoint adapter parameter")
	}

	bidder := &adapter{
		endpoint: config.Endpoint,
	}
	return bidder, nil
}

func (a *adapter) MakeRequests(request *openrtb2.BidRequest, reqInfo *adapters.ExtraRequestInfo) ([]*adapters.RequestData, []error) {
	var reqs []*adapters.RequestData
	var errs []error

	// group impressions by publisher ID
	publisherImps := make(map[string][]openrtb2.Imp)
	for _, imp := range request.Imp {
		extImp, err := getImpressionExt(&imp)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		impCopy := imp

		impCopy.Ext, err = jsonutil.Marshal(&impExtAdapter{
			Goldbach: *extImp,
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("error while encoding impression ext: %v", err))
			continue
		}

		publisherImps[extImp.PublisherID] = append(publisherImps[extImp.PublisherID], impCopy)
	}

	if len(publisherImps) == 0 {
		return nil, []error{errors.New("no publisher ID found in impressions")}
	}

	// create a separate request for each publisher
	for publisherID, imps := range publisherImps {
		requestPublisher := *request
		requestPublisher.Imp = imps
		requestPublisher.ID = fmt.Sprintf("%s_%s", request.ID, publisherID)

		resJSON, err := jsonutil.Marshal(&requestPublisher)
		if err != nil {
			return nil, []error{fmt.Errorf("error while encoding OpenRTB BidRequest: %v", err)}
		}

		headers := http.Header{}
		headers.Add("Content-Type", "application/json;charset=utf-8")
		headers.Add("Accept", "application/json")

		req := &adapters.RequestData{
			Method:  "POST",
			Uri:     a.endpoint,
			Body:    resJSON,
			Headers: headers,
			ImpIDs:  openrtb_ext.GetImpIDs(requestPublisher.Imp),
		}

		reqs = append(reqs, req)
	}

	return reqs, errs
}

func (a *adapter) MakeBids(bidReq *openrtb2.BidRequest, unused *adapters.RequestData, httpRes *adapters.ResponseData) (*adapters.BidderResponse, []error) {
	if httpRes.StatusCode == http.StatusNoContent {
		return nil, nil
	}

	if httpRes.StatusCode != http.StatusCreated {
		return nil, []error{
			fmt.Errorf("unexpected status code: %d. Run with request.debug = 1 for more info", httpRes.StatusCode),
		}
	}

	var resp openrtb2.BidResponse
	if err := jsonutil.Unmarshal(httpRes.Body, &resp); err != nil {
		return nil, []error{&errortypes.BadServerResponse{
			Message: fmt.Sprintf("Error while decoding response, err: %s", err),
		}}
	}

	bidderResponse := adapters.NewBidderResponse()
	bidderResponse.Currency = resp.Cur

	var errs []error
	for _, sb := range resp.SeatBid {
		for i := range sb.Bid {
			bidType, err := getBidMediaType(&sb.Bid[i])
			if err != nil {
				errs = append(errs, err)
				continue
			}

			bidderResponse.Bids = append(bidderResponse.Bids, &adapters.TypedBid{
				Bid:     &sb.Bid[i],
				BidType: bidType,
			})
		}
	}

	return bidderResponse, errs
}

func getImpressionExt(imp *openrtb2.Imp) (*openrtb_ext.ImpExtGoldbach, error) {
	var extImpBidder adapters.ExtImpBidder
	if err := jsonutil.Unmarshal(imp.Ext, &extImpBidder); err != nil {
		return nil, &errortypes.BadInput{
			Message: "missing ext",
		}
	}

	var goldbachExt openrtb_ext.ImpExtGoldbach
	if err := jsonutil.Unmarshal(extImpBidder.Bidder, &goldbachExt); err != nil {
		return nil, &errortypes.BadInput{
			Message: "missing ext.bidder",
		}
	}

	if len(goldbachExt.PublisherID) == 0 || len(goldbachExt.SlotID) == 0 {
		return nil, &errortypes.BadInput{
			Message: "publisher_id and slot_id are required",
		}
	}
	return &goldbachExt, nil
}

func getBidMediaType(bid *openrtb2.Bid) (openrtb_ext.BidType, error) {
	var extBid openrtb_ext.ExtBid
	if err := jsonutil.Unmarshal(bid.Ext, &extBid); err != nil {
		return "", fmt.Errorf("unable to deserialize imp %v bid.ext", bid.ImpID)
	}

	if extBid.Prebid == nil {
		return "", fmt.Errorf("imp %v with unknown media type", bid.ImpID)
	}

	return extBid.Prebid.Type, nil
}
