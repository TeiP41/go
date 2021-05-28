package serve

import (
	"context"
	"net/http"
	"testing"

	"github.com/stellar/go/amount"
	"github.com/stellar/go/clients/horizonclient"
	"github.com/stellar/go/keypair"
	"github.com/stellar/go/network"
	"github.com/stellar/go/protocols/horizon"
	"github.com/stellar/go/protocols/horizon/base"
	"github.com/stellar/go/services/regulated-assets-approval-server/internal/db/dbtest"
	"github.com/stellar/go/txnbuild"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTxApproveHandlerValidate(t *testing.T) {
	// empty asset issuer KP.
	h := txApproveHandler{}
	err := h.validate()
	require.EqualError(t, err, "issuer keypair cannot be nil")

	// empty asset code.
	issuerAccKeyPair := keypair.MustRandom()
	h = txApproveHandler{
		issuerKP: issuerAccKeyPair,
	}
	err = h.validate()
	require.EqualError(t, err, "asset code cannot be empty")

	// No Horizon client.
	h = txApproveHandler{
		issuerKP:  issuerAccKeyPair,
		assetCode: "FOOBAR",
	}
	err = h.validate()
	require.EqualError(t, err, "horizon client cannot be nil")

	// No network passphrase.
	horizonMock := horizonclient.MockClient{}
	h = txApproveHandler{
		issuerKP:      issuerAccKeyPair,
		assetCode:     "FOOBAR",
		horizonClient: &horizonMock,
	}
	err = h.validate()
	require.EqualError(t, err, "network passphrase cannot be empty")

	// No db.
	h = txApproveHandler{
		issuerKP:          issuerAccKeyPair,
		assetCode:         "FOOBAR",
		horizonClient:     &horizonMock,
		networkPassphrase: network.TestNetworkPassphrase,
	}
	err = h.validate()
	require.EqualError(t, err, "database cannot be nil")

	// Empty kycThreshold.
	db := dbtest.Open(t)
	defer db.Close()
	conn := db.Open()
	defer conn.Close()
	h = txApproveHandler{
		issuerKP:          issuerAccKeyPair,
		assetCode:         "FOOBAR",
		horizonClient:     &horizonMock,
		networkPassphrase: network.TestNetworkPassphrase,
		db:                conn,
	}
	err = h.validate()
	require.EqualError(t, err, "kyc threshold cannot be less than or equal to zero")

	// Negative kycThreshold.
	h = txApproveHandler{
		issuerKP:          issuerAccKeyPair,
		assetCode:         "FOOBAR",
		horizonClient:     &horizonMock,
		networkPassphrase: network.TestNetworkPassphrase,
		db:                conn,
		kycThreshold:      -1,
	}
	err = h.validate()
	require.EqualError(t, err, "kyc threshold cannot be less than or equal to zero")

	// no baseURL.
	h = txApproveHandler{
		issuerKP:          issuerAccKeyPair,
		assetCode:         "FOOBAR",
		horizonClient:     &horizonMock,
		networkPassphrase: network.TestNetworkPassphrase,
		db:                conn,
		kycThreshold:      1,
	}
	err = h.validate()
	require.EqualError(t, err, "base url cannot be empty")

	// Success.
	h = txApproveHandler{
		issuerKP:          issuerAccKeyPair,
		assetCode:         "FOOBAR",
		horizonClient:     &horizonMock,
		networkPassphrase: network.TestNetworkPassphrase,
		db:                conn,
		kycThreshold:      1,
		baseURL:           "https://sep8-server.test",
	}
	err = h.validate()
	require.NoError(t, err)
}

func TestTxApproveHandler_validateInput(t *testing.T) {
	h := txApproveHandler{}
	ctx := context.Background()

	// rejects if incoming tx is empty
	in := txApproveRequest{}
	txApprovalResp, gotTx := h.validateInput(ctx, in)
	require.Equal(t, NewRejectedTxApprovalResponse("Missing parameter \"tx\"."), txApprovalResp)
	require.Nil(t, gotTx)

	// rejects if incoming tx is invalid
	in = txApproveRequest{Tx: "foobar"}
	txApprovalResp, gotTx = h.validateInput(ctx, in)
	require.Equal(t, NewRejectedTxApprovalResponse("Invalid parameter \"tx\"."), txApprovalResp)
	require.Nil(t, gotTx)

	// rejects if incoming tx is a fee bump transaction
	in = txApproveRequest{Tx: "AAAABQAAAAAo/cVyQxyGh7F/Vsj0BzfDYuOJvrwgfHGyqYFpHB5RCAAAAAAAAADIAAAAAgAAAAAo/cVyQxyGh7F/Vsj0BzfDYuOJvrwgfHGyqYFpHB5RCAAAAGQAEfDJAAAAAQAAAAEAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAEAAAAAAAAAAQAAAAAo/cVyQxyGh7F/Vsj0BzfDYuOJvrwgfHGyqYFpHB5RCAAAAAAAAAAAAJiWgAAAAAAAAAAAAAAAAAAAAAA="}
	txApprovalResp, gotTx = h.validateInput(ctx, in)
	require.Equal(t, NewRejectedTxApprovalResponse("Invalid parameter \"tx\"."), txApprovalResp)
	require.Nil(t, gotTx)

	// rejects if tx source account is the issuer
	clientKP := keypair.MustRandom()
	h.issuerKP = keypair.MustRandom()

	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount: &horizon.Account{
			AccountID: h.issuerKP.Address(),
			Sequence:  "1",
		},
		IncrementSequenceNum: true,
		Timebounds:           txnbuild.NewInfiniteTimeout(),
		BaseFee:              300,
		Operations: []txnbuild.Operation{
			&txnbuild.Payment{
				Destination: clientKP.Address(),
				Amount:      "1",
				Asset:       txnbuild.NativeAsset{},
			},
		},
	})
	require.NoError(t, err)
	txe, err := tx.Base64()
	require.NoError(t, err)

	in.Tx = txe
	txApprovalResp, gotTx = h.validateInput(ctx, in)
	require.Equal(t, NewRejectedTxApprovalResponse("Transaction source account is invalid."), txApprovalResp)
	require.Nil(t, gotTx)

	// rejects if tx contains more than one operation
	tx, err = txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount: &horizon.Account{
			AccountID: clientKP.Address(),
			Sequence:  "1",
		},
		IncrementSequenceNum: true,
		Timebounds:           txnbuild.NewInfiniteTimeout(),
		BaseFee:              300,
		Operations: []txnbuild.Operation{
			&txnbuild.BumpSequence{},
			&txnbuild.Payment{
				Destination: clientKP.Address(),
				Amount:      "1.0000000",
				Asset:       txnbuild.NativeAsset{},
			},
		},
	})
	require.NoError(t, err)
	txe, err = tx.Base64()
	require.NoError(t, err)

	in.Tx = txe
	txApprovalResp, gotTx = h.validateInput(ctx, in)
	require.Equal(t, NewRejectedTxApprovalResponse("Please submit a transaction with exactly one operation of type payment."), txApprovalResp)
	require.Nil(t, gotTx)

	// success
	tx, err = txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount: &horizon.Account{
			AccountID: clientKP.Address(),
			Sequence:  "1",
		},
		IncrementSequenceNum: true,
		Timebounds:           txnbuild.NewInfiniteTimeout(),
		BaseFee:              300,
		Operations: []txnbuild.Operation{
			&txnbuild.Payment{
				Destination: clientKP.Address(),
				Amount:      "1.0000000",
				Asset:       txnbuild.NativeAsset{},
			},
		},
	})
	require.NoError(t, err)
	txe, err = tx.Base64()
	require.NoError(t, err)

	in.Tx = txe
	txApprovalResp, gotTx = h.validateInput(ctx, in)
	require.Nil(t, txApprovalResp)
	require.Equal(t, gotTx, tx)
}

func TestConvertAmountToReadableString(t *testing.T) {
	parsedAmount, err := amount.ParseInt64("500")
	require.NoError(t, err)
	assert.Equal(t, int64(5000000000), parsedAmount)

	readableAmount, err := convertAmountToReadableString(parsedAmount)
	require.NoError(t, err)
	assert.Equal(t, "500.00", readableAmount)
}

func TestTxApproveHandler_handleActionRequiredResponseIfNeeded(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	defer db.Close()
	conn := db.Open()
	defer conn.Close()

	kycThreshold, err := amount.ParseInt64("500")
	require.NoError(t, err)
	h := txApproveHandler{
		assetCode:    "FOO",
		baseURL:      "https://sep8-server.test",
		kycThreshold: kycThreshold,
		db:           conn,
	}

	// payments smaller than or equal the threshold are not "action_required"
	clientKP := keypair.MustRandom()
	paymentOp := &txnbuild.Payment{
		Amount: amount.StringFromInt64(kycThreshold),
	}
	txApprovalResp, err := h.handleActionRequiredResponseIfNeeded(ctx, clientKP.Address(), paymentOp)
	require.NoError(t, err)
	require.Nil(t, txApprovalResp)

	// payments greater than the threshold are "action_required"
	paymentOp = &txnbuild.Payment{
		Amount: amount.StringFromInt64(kycThreshold + 1),
	}
	txApprovalResp, err = h.handleActionRequiredResponseIfNeeded(ctx, clientKP.Address(), paymentOp)
	require.NoError(t, err)

	var callbackID string
	q := `SELECT callback_id FROM accounts_kyc_status WHERE stellar_address = $1`
	err = conn.QueryRowContext(ctx, q, clientKP.Address()).Scan(&callbackID)
	require.NoError(t, err)

	wantResp := &txApprovalResponse{
		Status:       sep8StatusActionRequired,
		Message:      "Payments exceeding 500.00 FOO require KYC approval. Please provide an email address.",
		ActionMethod: "POST",
		StatusCode:   http.StatusOK,
		ActionURL:    "https://sep8-server.test/kyc-status/" + callbackID,
		ActionFields: []string{"email_address"},
	}
	require.Equal(t, wantResp, txApprovalResp)

	// test addresses with approved KYC
	q = `
		UPDATE accounts_kyc_status
		SET 
			approved_at = NOW(),
			rejected_at = NULL
		WHERE stellar_address = $1
	`
	_, err = conn.ExecContext(ctx, q, clientKP.Address())
	require.NoError(t, err)
	txApprovalResp, err = h.handleActionRequiredResponseIfNeeded(ctx, clientKP.Address(), paymentOp)
	require.NoError(t, err)
	require.Nil(t, txApprovalResp)

	// test addresses with rejected KYC
	q = `
		UPDATE accounts_kyc_status
		SET 
			approved_at = NULL,
			rejected_at = NOW()
		WHERE stellar_address = $1
	`
	_, err = conn.ExecContext(ctx, q, clientKP.Address())
	require.NoError(t, err)
	txApprovalResp, err = h.handleActionRequiredResponseIfNeeded(ctx, clientKP.Address(), paymentOp)
	require.NoError(t, err)
	require.Equal(t, NewRejectedTxApprovalResponse("Your KYC was rejected and you're not authorized for operations above 500.00 FOO."), txApprovalResp)
}

func TestTxApproveHandlerTxApprove(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	defer db.Close()
	conn := db.Open()
	defer conn.Close()

	// Perpare accounts on mock horizon.
	issuerAccKeyPair := keypair.MustRandom()
	senderAccKP := keypair.MustRandom()
	receiverAccKP := keypair.MustRandom()
	assetGOAT := txnbuild.CreditAsset{
		Code:   "GOAT",
		Issuer: issuerAccKeyPair.Address(),
	}
	horizonMock := horizonclient.MockClient{}
	horizonMock.
		On("AccountDetail", horizonclient.AccountRequest{AccountID: issuerAccKeyPair.Address()}).
		Return(horizon.Account{
			AccountID: issuerAccKeyPair.Address(),
			Sequence:  "1",
			Balances: []horizon.Balance{
				{
					Asset:   base.Asset{Code: "ASSET", Issuer: issuerAccKeyPair.Address()},
					Balance: "0",
				},
			},
		}, nil)
	horizonMock.
		On("AccountDetail", horizonclient.AccountRequest{AccountID: senderAccKP.Address()}).
		Return(horizon.Account{
			AccountID: senderAccKP.Address(),
			Sequence:  "2",
		}, nil)
	horizonMock.
		On("AccountDetail", horizonclient.AccountRequest{AccountID: receiverAccKP.Address()}).
		Return(horizon.Account{
			AccountID: receiverAccKP.Address(),
			Sequence:  "3",
		}, nil)

	// Create tx-approve/ txApproveHandler.
	kycThresholdAmount, err := amount.ParseInt64("500")
	require.NoError(t, err)
	handler := txApproveHandler{
		issuerKP:          issuerAccKeyPair,
		assetCode:         assetGOAT.GetCode(),
		horizonClient:     &horizonMock,
		networkPassphrase: network.TestNetworkPassphrase,
		db:                conn,
		kycThreshold:      kycThresholdAmount,
		baseURL:           "https://sep8-server.test",
	}

	// TEST "rejected" response if no transaction is submitted; with empty "tx" for txApprove.
	req := txApproveRequest{
		Tx: "",
	}
	rejectedResponse, err := handler.txApprove(ctx, req)
	require.NoError(t, err)
	wantRejectedResponse := txApprovalResponse{
		Status:     "rejected",
		Error:      `Missing parameter "tx".`,
		StatusCode: http.StatusBadRequest,
	}
	assert.Equal(t, &wantRejectedResponse, rejectedResponse)

	// TEST "rejected" response if can't parse XDR; with malformed "tx" for txApprove.
	req = txApproveRequest{
		Tx: "BADXDRTRANSACTIONENVELOPE",
	}
	rejectedResponse, err = handler.txApprove(ctx, req)
	require.NoError(t, err)
	wantRejectedResponse = txApprovalResponse{
		Status:     "rejected",
		Error:      `Invalid parameter "tx".`,
		StatusCode: http.StatusBadRequest,
	}
	assert.Equal(t, &wantRejectedResponse, rejectedResponse)

	// Prepare invalid(non generic transaction) "tx" for txApprove.
	senderAcc, err := handler.horizonClient.AccountDetail(horizonclient.AccountRequest{AccountID: senderAccKP.Address()})
	require.NoError(t, err)
	tx, err := txnbuild.NewTransaction(
		txnbuild.TransactionParams{
			SourceAccount:        &senderAcc,
			IncrementSequenceNum: true,
			Operations: []txnbuild.Operation{
				&txnbuild.Payment{
					Destination: receiverAccKP.Address(),
					Amount:      "1",
					Asset:       assetGOAT,
				},
			},
			BaseFee:    txnbuild.MinBaseFee,
			Timebounds: txnbuild.NewInfiniteTimeout(),
		},
	)
	require.NoError(t, err)
	feeBumpTx, err := txnbuild.NewFeeBumpTransaction(
		txnbuild.FeeBumpTransactionParams{
			Inner:      tx,
			FeeAccount: receiverAccKP.Address(),
			BaseFee:    2 * txnbuild.MinBaseFee,
		},
	)
	require.NoError(t, err)
	feeBumpTxEnc, err := feeBumpTx.Base64()
	require.NoError(t, err)

	// TEST "rejected" response if a non generic transaction fails, same result as malformed XDR.
	req = txApproveRequest{
		Tx: feeBumpTxEnc,
	}
	rejectedResponse, err = handler.txApprove(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, &wantRejectedResponse, rejectedResponse) // wantRejectedResponse is identical to "if can't parse XDR".

	// Prepare transaction sourceAccount the same as the server issuer account for txApprove.
	issuerAcc, err := handler.horizonClient.AccountDetail(horizonclient.AccountRequest{AccountID: issuerAccKeyPair.Address()})
	require.NoError(t, err)
	tx, err = txnbuild.NewTransaction(
		txnbuild.TransactionParams{
			SourceAccount:        &issuerAcc,
			IncrementSequenceNum: true,
			Operations: []txnbuild.Operation{
				&txnbuild.Payment{
					Destination: senderAccKP.Address(),
					Amount:      "1",
					Asset:       assetGOAT,
				},
			},
			BaseFee:    txnbuild.MinBaseFee,
			Timebounds: txnbuild.NewInfiniteTimeout(),
		},
	)
	require.NoError(t, err)
	txEnc, err := tx.Base64()
	require.NoError(t, err)

	// TEST "rejected" response for sender account; transaction sourceAccount the same as the server issuer account.
	req = txApproveRequest{
		Tx: txEnc,
	}
	rejectedResponse, err = handler.txApprove(ctx, req)
	require.NoError(t, err)
	wantRejectedResponse = txApprovalResponse{
		Status:     "rejected",
		Error:      "Transaction source account is invalid.",
		StatusCode: http.StatusBadRequest,
	}
	assert.Equal(t, &wantRejectedResponse, rejectedResponse)

	// Prepare transaction where transaction's payment operation sourceAccount the same as the server issuer account.
	tx, err = txnbuild.NewTransaction(
		txnbuild.TransactionParams{
			SourceAccount:        &senderAcc,
			IncrementSequenceNum: true,
			Operations: []txnbuild.Operation{
				&txnbuild.Payment{
					SourceAccount: issuerAccKeyPair.Address(),
					Destination:   senderAccKP.Address(),
					Amount:        "1",
					Asset:         assetGOAT,
				},
			},
			BaseFee:    txnbuild.MinBaseFee,
			Timebounds: txnbuild.NewInfiniteTimeout(),
		},
	)
	require.NoError(t, err)
	txEnc, err = tx.Base64()
	require.NoError(t, err)

	// TEST "rejected" response for sender account; payment operation sourceAccount the same as the server issuer account.
	req = txApproveRequest{
		Tx: txEnc,
	}
	rejectedResponse, err = handler.txApprove(ctx, req)
	require.NoError(t, err)
	wantRejectedResponse = txApprovalResponse{
		Status:     "rejected",
		Error:      "There is one or more unauthorized operations in the provided transaction.",
		StatusCode: http.StatusBadRequest,
	}
	assert.Equal(t, &wantRejectedResponse, rejectedResponse)

	// Prepare transaction where operation is not a payment (in this case allowing trust for receiverAccKP).
	tx, err = txnbuild.NewTransaction(
		txnbuild.TransactionParams{
			SourceAccount:        &senderAcc,
			IncrementSequenceNum: true,
			Operations: []txnbuild.Operation{
				&txnbuild.AllowTrust{
					Trustor:   receiverAccKP.Address(),
					Type:      assetGOAT,
					Authorize: true,
				},
			},
			BaseFee:    txnbuild.MinBaseFee,
			Timebounds: txnbuild.NewInfiniteTimeout(),
		},
	)
	require.NoError(t, err)
	txEnc, err = tx.Base64()

	// TEST "rejected" response if operation is not a payment (in this case allowing trust for receiverAccKP).
	req = txApproveRequest{
		Tx: txEnc,
	}
	rejectedResponse, err = handler.txApprove(ctx, req)
	require.NoError(t, err)
	wantRejectedResponse = txApprovalResponse{
		Status:     "rejected",
		Error:      "There is one or more unauthorized operations in the provided transaction.",
		StatusCode: http.StatusBadRequest,
	}
	assert.Equal(t, &wantRejectedResponse, rejectedResponse)

	// Prepare transaction with multiple operations.
	tx, err = txnbuild.NewTransaction(
		txnbuild.TransactionParams{
			SourceAccount:        &senderAcc,
			IncrementSequenceNum: true,
			Operations: []txnbuild.Operation{
				&txnbuild.Payment{
					SourceAccount: senderAccKP.Address(),
					Destination:   receiverAccKP.Address(),
					Amount:        "1",
					Asset:         assetGOAT,
				},
				&txnbuild.Payment{
					SourceAccount: senderAccKP.Address(),
					Destination:   receiverAccKP.Address(),
					Amount:        "2",
					Asset:         assetGOAT,
				},
			},
			BaseFee:    txnbuild.MinBaseFee,
			Timebounds: txnbuild.NewInfiniteTimeout(),
		},
	)
	require.NoError(t, err)
	txEnc, err = tx.Base64()
	require.NoError(t, err)

	// TEST "rejected" response for sender account; transaction with multiple operations.
	req = txApproveRequest{
		Tx: txEnc,
	}
	rejectedResponse, err = handler.txApprove(ctx, req)
	require.NoError(t, err)
	wantRejectedResponse = txApprovalResponse{
		Status:     "rejected",
		Error:      "Please submit a transaction with exactly one operation of type payment.",
		StatusCode: http.StatusBadRequest,
	}
	assert.Equal(t, &wantRejectedResponse, rejectedResponse)

	// Prepare transaction where sourceAccount seq num too far in the future.
	tx, err = txnbuild.NewTransaction(
		txnbuild.TransactionParams{
			SourceAccount: &horizon.Account{
				AccountID: senderAccKP.Address(),
				Sequence:  "50",
			},
			IncrementSequenceNum: true,
			Operations: []txnbuild.Operation{
				&txnbuild.Payment{
					SourceAccount: senderAccKP.Address(),
					Destination:   receiverAccKP.Address(),
					Amount:        "1",
					Asset:         assetGOAT,
				},
			},
			BaseFee:    txnbuild.MinBaseFee,
			Timebounds: txnbuild.NewInfiniteTimeout(),
		},
	)
	require.NoError(t, err)
	txEnc, err = tx.Base64()
	require.NoError(t, err)

	// TEST "rejected" response if transaction source account seq num is not equal to account sequence+1.
	req = txApproveRequest{
		Tx: txEnc,
	}
	rejectedResponse, err = handler.txApprove(ctx, req)
	require.NoError(t, err)
	wantRejectedResponse = txApprovalResponse{
		Status:     "rejected",
		Error:      "Invalid transaction sequence number.",
		StatusCode: http.StatusBadRequest,
	}
	assert.Equal(t, &wantRejectedResponse, rejectedResponse)
}
