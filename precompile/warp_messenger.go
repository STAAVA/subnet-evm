// (c) 2022-2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package precompile

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"

	"github.com/ava-labs/avalanchego/utils/set"
	"github.com/ava-labs/avalanchego/vms/platformvm/teleporter"
	"github.com/ava-labs/subnet-evm/accounts/abi"
	"github.com/ava-labs/subnet-evm/vmerrs"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"

	"github.com/ethereum/go-ethereum/common"
)

const (
	GetBlockchainIdGasCost                        uint64 = 5_000
	GetVerifiedWarpMessageGasCost                 uint64 = 100_000
	GetVerifiedWarpMessageGasCostPerAggregatedKey uint64 = 1_000
	SendWarpMessageGasCost                        uint64 = 100_000

	// WarpMessengerRawABI contains the raw ABI of WarpMessenger contract.
	WarpMessengerRawABI  = "[{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"bytes32\",\"name\":\"destinationChainId\",\"type\":\"bytes32\"},{\"indexed\":true,\"internalType\":\"bytes32\",\"name\":\"destinationAddress\",\"type\":\"bytes32\"},{\"indexed\":true,\"internalType\":\"bytes32\",\"name\":\"sender\",\"type\":\"bytes32\"},{\"indexed\":false,\"internalType\":\"bytes\",\"name\":\"message\",\"type\":\"bytes\"}],\"name\":\"SendWarpMessage\",\"type\":\"event\"},{\"inputs\":[],\"name\":\"getBlockchainId\",\"outputs\":[{\"internalType\":\"bytes32\",\"name\":\"blockchainId\",\"type\":\"bytes32\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"messageIndex\",\"type\":\"uint256\"}],\"name\":\"getVerifiedWarpMessage\",\"outputs\":[{\"components\":[{\"internalType\":\"bytes32\",\"name\":\"originChainId\",\"type\":\"bytes32\"},{\"internalType\":\"bytes32\",\"name\":\"originSenderAddress\",\"type\":\"bytes32\"},{\"internalType\":\"bytes32\",\"name\":\"destinationChainId\",\"type\":\"bytes32\"},{\"internalType\":\"bytes32\",\"name\":\"destinationAddress\",\"type\":\"bytes32\"},{\"internalType\":\"bytes\",\"name\":\"payload\",\"type\":\"bytes\"}],\"internalType\":\"structWarpMessage\",\"name\":\"message\",\"type\":\"tuple\"},{\"internalType\":\"bool\",\"name\":\"success\",\"type\":\"bool\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"bytes32\",\"name\":\"destinationChainId\",\"type\":\"bytes32\"},{\"internalType\":\"bytes32\",\"name\":\"destinationAddress\",\"type\":\"bytes32\"},{\"internalType\":\"bytes\",\"name\":\"payload\",\"type\":\"bytes\"}],\"name\":\"sendWarpMessage\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"}]"
	SubmitMessageEventID = "da2b1cd3e6664863b4ad90f53a4e14fca9fc00f3f0e01e5c7b236a4355b6591a" // Keccack256("SubmitMessage(bytes32,uint256)")

	// Default stake threshold for aggregate signature verification. (67%)
	// TODO: This should be made configuration on the VM level.
	WarpQuorumNumerator   = 67
	WarpQuorumDenominator = 100
)

// Reference imports to suppress errors from unused imports. This code and any unnecessary imports can be removed.
var (
	_ = errors.New
	_ = big.NewInt
	_ = strings.NewReader
)

// Singleton StatefulPrecompiledContract and signatures.
var (
	_ StatefulPrecompileConfig = &WarpMessengerConfig{}

	WarpMessengerABI abi.ABI // will be initialized by init function

	WarpMessengerPrecompile StatefulPrecompiledContract // will be initialized by init function
)

// WarpMessengerConfig implements the StatefulPrecompileConfig
// interface while adding in the WarpMessenger specific precompile address.
type WarpMessengerConfig struct {
	UpgradeableConfig
}

// WarpMessage is an auto generated low-level Go binding around an user-defined struct.
type WarpMessage struct {
	OriginChainId       [32]byte
	OriginSenderAddress [32]byte
	DestinationChainId  [32]byte
	DestinationAddress  [32]byte
	Payload             []byte
}

type GetVerifiedWarpMessageOutput struct {
	Message WarpMessage
	Success bool
}

type SendWarpMessageInput struct {
	DestinationChainId [32]byte
	DestinationAddress [32]byte
	Payload            []byte
}

func init() {
	parsed, err := abi.JSON(strings.NewReader(WarpMessengerRawABI))
	if err != nil {
		panic(err)
	}
	WarpMessengerABI = parsed

	WarpMessengerPrecompile = createWarpMessengerPrecompile(WarpMessengerAddress)
}

// NewWarpMessengerConfig returns a config for a network upgrade at [blockTimestamp] that enables
// WarpMessenger .
func NewWarpMessengerConfig(blockTimestamp *big.Int) *WarpMessengerConfig {
	return &WarpMessengerConfig{

		UpgradeableConfig: UpgradeableConfig{BlockTimestamp: blockTimestamp},
	}
}

// NewDisableWarpMessengerConfig returns config for a network upgrade at [blockTimestamp]
// that disables WarpMessenger.
func NewDisableWarpMessengerConfig(blockTimestamp *big.Int) *WarpMessengerConfig {
	return &WarpMessengerConfig{
		UpgradeableConfig: UpgradeableConfig{
			BlockTimestamp: blockTimestamp,
			Disable:        true,
		},
	}
}

// Equal returns true if [s] is a [*WarpMessengerConfig] and it has been configured identical to [c].
func (c *WarpMessengerConfig) Equal(s StatefulPrecompileConfig) bool {
	// typecast before comparison
	other, ok := (s).(*WarpMessengerConfig)
	if !ok {
		return false
	}
	
	// CUSTOM CODE STARTS HERE
	// modify this boolean accordingly with your custom WarpMessengerConfig, to check if [other] and the current [c] are equal
	// if WarpMessengerConfig contains only UpgradeableConfig  you can skip modifying it.
	equals := c.UpgradeableConfig.Equal(&other.UpgradeableConfig)
	return equals
}

// String returns a string representation of the WarpMessengerConfig.
func (c *WarpMessengerConfig) String() string {
	bytes, _ := json.Marshal(c)
	return string(bytes)
}

// Address returns the address of the WarpMessenger. Addresses reside under the precompile/params.go
// Select a non-conflicting address and set it in the params.go.
func (c *WarpMessengerConfig) Address() common.Address {
	return WarpMessengerAddress
}

// Configure configures [state] with the initial configuration.
func (c *WarpMessengerConfig) Configure(_ ChainConfig, state StateDB, _ BlockContext) {

	// CUSTOM CODE STARTS HERE
}

// Contract returns the singleton stateful precompiled contract to be used for WarpMessenger.
func (c *WarpMessengerConfig) Contract() StatefulPrecompiledContract {
	return WarpMessengerPrecompile
}

// Verify tries to verify WarpMessengerConfig and returns an error accordingly.
func (c *WarpMessengerConfig) Verify() error {

	// CUSTOM CODE STARTS HERE
	// Add your own custom verify code for WarpMessengerConfig here
	// and return an error accordingly
	return nil
}

// Predicate optionally returns a function to enforce as a predicate for a transaction to be valid
// if the access list of the transaction includes a tuple that references the precompile address.
// Returns nil here to indicate that this precompile does not enforce a predicate.
func (c *WarpMessengerConfig) Predicate() PredicateFunc {
	return c.verifyPredicate
}

func (c *WarpMessengerConfig) verifyPredicate(predicateContext *PredicateContext, storageSlots []byte) error {
	// The proposer VM block context is required to verify aggregate signatures.
	if predicateContext.ProposerVMBlockCtx == nil {
		return errors.New("missing proposer VM block context")
	}

	// If there are no storage slots, we consider the predicate to be valid because
	// there are no messages to be received.
	if len(storageSlots) == 0 {
		return nil
	}

	// RLP decode the list of signed messages.
	var messagesBytes [][]byte
	err := rlp.DecodeBytes(storageSlots, &messagesBytes)
	if err != nil {
		return err
	}

	// Check that each message was intended for this chain, and verify the aggregate signature of each message.
	for _, messageBytes := range messagesBytes {
		message, err := teleporter.ParseMessage(messageBytes)
		if err != nil {
			return err
		}

		// TODO: Should we add a special chain ID that is allowed as the "anycast" chain ID? Just need to think through if there are any security implications.
		if message.DestinationChainID != predicateContext.SnowCtx.ChainID {
			return errors.New("wrong chain id")
		}

		err = message.Signature.Verify(
			context.Background(),
			&message.UnsignedMessage,
			predicateContext.SnowCtx.ValidatorState,
			predicateContext.ProposerVMBlockCtx.PChainHeight,
			WarpQuorumNumerator,
			WarpQuorumDenominator)
		if err != nil {
			return err
		}
		log.Info("Warp message verification passed.")
	}

	log.Info("Warp precompile predicate passed.")
	return nil
}

// OnAccept optionally returns a function to perform on any log with the precompile address.
// If enabled, this will be called after the block is accepted to perform post-accept computation.
func (c *WarpMessengerConfig) OnAccept() OnAcceptFunc {
	return nil
}

// PackGetBlockchainId packs the include selector (first 4 func signature bytes).
// This function is mostly used for tests.
func PackGetBlockchainId() ([]byte, error) {
	return WarpMessengerABI.Pack("getBlockchainId")
}

// PackGetBlockchainIdOutput attempts to pack given blockchainId of type [32]byte
// to conform the ABI outputs.
func PackGetBlockchainIdOutput(blockchainId [32]byte) ([]byte, error) {
	return WarpMessengerABI.PackOutput("getBlockchainId", blockchainId)
}

func getBlockchainId(accessibleState PrecompileAccessibleState, caller common.Address, addr common.Address, input []byte, suppliedGas uint64, readOnly bool) (ret []byte, remainingGas uint64, err error) {
	if remainingGas, err = deductGas(suppliedGas, GetBlockchainIdGasCost); err != nil {
		return nil, 0, err
	}

	if readOnly {
		return nil, remainingGas, vmerrs.ErrWriteProtection
	}

	packedOutput, err := PackGetBlockchainIdOutput(accessibleState.GetSnowContext().ChainID)
	if err != nil {
		return nil, remainingGas, err
	}

	// Return the packed output and the remaining gas
	return packedOutput, remainingGas, nil
}

// UnpackGetVerifiedWarpMessageInput attempts to unpack [input] into the *big.Int type argument
// assumes that [input] does not include selector (omits first 4 func signature bytes)
func UnpackGetVerifiedWarpMessageInput(input []byte) (*big.Int, error) {
	res, err := WarpMessengerABI.UnpackInput("getVerifiedWarpMessage", input)
	if err != nil {
		return big.NewInt(0), err
	}
	unpacked := *abi.ConvertType(res[0], new(*big.Int)).(**big.Int)
	return unpacked, nil
}

// PackGetVerifiedWarpMessage packs [messageIndex] of type *big.Int into the appropriate arguments for getVerifiedWarpMessage.
// the packed bytes include selector (first 4 func signature bytes).
// This function is mostly used for tests.
func PackGetVerifiedWarpMessage(messageIndex *big.Int) ([]byte, error) {
	return WarpMessengerABI.Pack("getVerifiedWarpMessage", messageIndex)
}

// PackGetVerifiedWarpMessageOutput attempts to pack given [outputStruct] of type GetVerifiedWarpMessageOutput
// to conform the ABI outputs.
func PackGetVerifiedWarpMessageOutput(outputStruct GetVerifiedWarpMessageOutput) ([]byte, error) {
	return WarpMessengerABI.PackOutput("getVerifiedWarpMessage",
		outputStruct.Message,
		outputStruct.Success,
	)
}

func getVerifiedWarpMessage(accessibleState PrecompileAccessibleState, caller common.Address, addr common.Address, input []byte, suppliedGas uint64, readOnly bool) (ret []byte, remainingGas uint64, err error) {
	if remainingGas, err = deductGas(suppliedGas, GetVerifiedWarpMessageGasCost); err != nil {
		return nil, 0, err
	}
	if readOnly {
		return nil, remainingGas, vmerrs.ErrWriteProtection
	}
	// attempts to unpack [input] into the arguments to the GetVerifiedWarpMessageInput.
	// Assumes that [input] does not include selector
	// You can use unpacked [messageIndex] variable in your code
	inputIndex, err := UnpackGetVerifiedWarpMessageInput(input)
	if err != nil {
		return nil, remainingGas, err
	}

	storageSlots, exists := accessibleState.GetStateDB().GetPredicateStorageSlots(WarpMessengerAddress)
	if !exists || storageSlots == nil {
		return nil, remainingGas, errors.New("missing access list storage slots from precompile during execution")
	}

	var signedMessages [][]byte
	err = rlp.DecodeBytes(storageSlots, &signedMessages)
	if err != nil {
		return nil, remainingGas, err
	}

	// Check that the message index exists.
	if !inputIndex.IsInt64() {
		return nil, remainingGas, errors.New("invalid message index")
	}
	messageIndex := inputIndex.Int64()
	if len(signedMessages) <= int(messageIndex) {
		return nil, remainingGas, errors.New("invalid message index")
	}

	// Parse the raw message to be processed.
	signedMessage := signedMessages[messageIndex]
	message, err := teleporter.ParseMessage(signedMessage)
	if err != nil {
		return nil, remainingGas, err
	}

	// Charge gas per validator included in the aggregate signature
	bitSetSignature, ok := message.Signature.(*teleporter.BitSetSignature)
	if !ok {
		return nil, remainingGas, errors.New("invalid aggregate signature")
	}

	numSigners := set.BitsFromBytes(bitSetSignature.Signers).HammingWeight()
	if remainingGas, err = deductGas(remainingGas, GetVerifiedWarpMessageGasCost*uint64(numSigners)); err != nil {
		return nil, 0, err
	}

	var warpMessage WarpMessage
	_, err = Codec.Unmarshal(message.Payload, &warpMessage)
	if err != nil {
		return nil, remainingGas, err
	}

	output := GetVerifiedWarpMessageOutput{
		Message: warpMessage,
		Success: true,
	}

	packedOutput, err := PackGetVerifiedWarpMessageOutput(output)
	if err != nil {
		return nil, remainingGas, err
	}

	// Return the packed output and the remaining gas
	return packedOutput, remainingGas, nil
}

// UnpackSendWarpMessageInput attempts to unpack [input] into the arguments for the SendWarpMessageInput{}
// assumes that [input] does not include selector (omits first 4 func signature bytes)
func UnpackSendWarpMessageInput(input []byte) (SendWarpMessageInput, error) {
	inputStruct := SendWarpMessageInput{}
	err := WarpMessengerABI.UnpackInputIntoInterface(&inputStruct, "sendWarpMessage", input)

	return inputStruct, err
}

// PackSendWarpMessage packs [inputStruct] of type SendWarpMessageInput into the appropriate arguments for sendWarpMessage.
func PackSendWarpMessage(inputStruct SendWarpMessageInput) ([]byte, error) {
	return WarpMessengerABI.Pack("sendWarpMessage", inputStruct.DestinationChainId, inputStruct.DestinationAddress, inputStruct.Payload)
}

func sendWarpMessage(accessibleState PrecompileAccessibleState, caller common.Address, addr common.Address, input []byte, suppliedGas uint64, readOnly bool) (ret []byte, remainingGas uint64, err error) {
	if remainingGas, err = deductGas(suppliedGas, SendWarpMessageGasCost); err != nil {
		return nil, 0, err
	}
	if readOnly {
		return nil, remainingGas, vmerrs.ErrWriteProtection
	}
	// attempts to unpack [input] into the arguments to the SendWarpMessageInput.
	// Assumes that [input] does not include selector
	// You can use unpacked [inputStruct] variable in your code
	inputStruct, err := UnpackSendWarpMessageInput(input)
	if err != nil {
		return nil, remainingGas, err
	}

	message := &WarpMessage{
		OriginChainId:       accessibleState.GetSnowContext().ChainID,
		OriginSenderAddress: caller.Hash(),
		DestinationChainId:  inputStruct.DestinationChainId,
		DestinationAddress:  inputStruct.DestinationAddress,
		Payload:             inputStruct.Payload,
	}

	data, err := Codec.Marshal(Version, message)
	if err != nil {
		return nil, remainingGas, err
	}

	accessibleState.GetStateDB().AddLog(
		WarpMessengerAddress,
		[]common.Hash{
			common.HexToHash(SubmitMessageEventID),
			message.OriginChainId,
			message.DestinationChainId,
		},
		data,
		accessibleState.GetBlockContext().Number().Uint64())

	return []byte{}, remainingGas, nil
}

// createWarpMessengerPrecompile returns a StatefulPrecompiledContract with getters and setters for the precompile.
func createWarpMessengerPrecompile(precompileAddr common.Address) StatefulPrecompiledContract {
	var functions []*statefulPrecompileFunction

	methodGetBlockchainId, ok := WarpMessengerABI.Methods["getBlockchainId"]
	if !ok {
		panic("given method does not exist in the ABI")
	}
	functions = append(functions, newStatefulPrecompileFunction(methodGetBlockchainId.ID, getBlockchainId))

	methodGetVerifiedWarpMessage, ok := WarpMessengerABI.Methods["getVerifiedWarpMessage"]
	if !ok {
		panic("given method does not exist in the ABI")
	}
	functions = append(functions, newStatefulPrecompileFunction(methodGetVerifiedWarpMessage.ID, getVerifiedWarpMessage))

	methodSendWarpMessage, ok := WarpMessengerABI.Methods["sendWarpMessage"]
	if !ok {
		panic("given method does not exist in the ABI")
	}
	functions = append(functions, newStatefulPrecompileFunction(methodSendWarpMessage.ID, sendWarpMessage))

	// Construct the contract with no fallback function.
	contract := newStatefulPrecompileWithFunctionSelectors(nil, functions)
	return contract
}