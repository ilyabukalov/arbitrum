/*
 * Copyright 2019, Offchain Labs, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *    http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package ethbridge

import (
	"bytes"
	"context"
	"errors"
	"math/big"

	"github.com/offchainlabs/arbitrum/packages/arb-util/machine"

	"github.com/offchainlabs/arbitrum/packages/arb-validator/ethbridge/chainlauncher"

	"github.com/offchainlabs/arbitrum/packages/arb-validator/valmessage"
	errors2 "github.com/pkg/errors"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	solsha3 "github.com/miguelmota/go-solidity-sha3"

	"github.com/offchainlabs/arbitrum/packages/arb-util/protocol"
	"github.com/offchainlabs/arbitrum/packages/arb-util/value"
	"github.com/offchainlabs/arbitrum/packages/arb-validator/ethbridge/challengemanager"
	"github.com/offchainlabs/arbitrum/packages/arb-validator/hashing"
)

type ArbitrumVM struct {
	OutChan            chan Notification
	ErrChan            chan error
	Client             *ethclient.Client
	ArbitrumVM         *chainlauncher.ArbitrumVM
	Challenge          *challengemanager.ChallengeManager
	GlobalPendingInbox *chainlauncher.IGlobalPendingInbox

	address common.Address
	client  *ethclient.Client
}

func NewArbitrumVM(address common.Address, client *ethclient.Client) (*ArbitrumVM, error) {
	outChan := make(chan Notification, 1024)
	errChan := make(chan error, 1024)
	vm := &ArbitrumVM{OutChan: outChan, ErrChan: errChan, Client: client, address: address}
	err := vm.setupContracts()
	return vm, err
}

func (vm *ArbitrumVM) setupContracts() error {
	arbitrumVMContract, err := chainlauncher.NewArbitrumVM(vm.address, vm.Client)
	if err != nil {
		return errors2.Wrap(err, "Failed to connect to ArbChannel")
	}

	challengeManagerAddress, err := arbitrumVMContract.ChallengeManager(&bind.CallOpts{
		Pending: false,
		Context: context.Background(),
	})
	if err != nil {
		return errors2.Wrap(err, "Failed to get ChallengeManager address")
	}
	challengeManagerContract, err := challengemanager.NewChallengeManager(challengeManagerAddress, vm.Client)
	if err != nil {
		return errors2.Wrap(err, "Failed to connect to ChallengeManager")
	}

	globalPendingInboxAddress, err := arbitrumVMContract.GlobalInbox(&bind.CallOpts{
		Pending: false,
		Context: context.Background(),
	})
	if err != nil {
		return errors2.Wrap(err, "Failed to get GlobalPendingInbox address")
	}
	globalPendingContract, err := chainlauncher.NewIGlobalPendingInbox(globalPendingInboxAddress, vm.Client)
	if err != nil {
		return errors2.Wrap(err, "Failed to connect to GlobalPendingInbox")
	}

	vm.ArbitrumVM = arbitrumVMContract
	vm.Challenge = challengeManagerContract
	vm.GlobalPendingInbox = globalPendingContract
	return nil
}

func (vm *ArbitrumVM) GetChans() (chan Notification, chan error) {
	return vm.OutChan, vm.ErrChan
}

func (vm *ArbitrumVM) Close() {
	close(vm.OutChan)
	close(vm.ErrChan)
}

func (vm *ArbitrumVM) StartConnection(ctx context.Context) error {
	if err := vm.setupContracts(); err != nil {
		return err
	}

	start := uint64(0)
	watch := &bind.WatchOpts{
		Context: ctx,
		Start:   &start,
	}

	headers := make(chan *types.Header)
	headersSub, err := vm.Client.SubscribeNewHead(ctx, headers)
	if err != nil {
		return err
	}

	messageDeliveredChan := make(chan *chainlauncher.IGlobalPendingInboxMessageDelivered)
	messageDeliveredSub, err := vm.GlobalPendingInbox.WatchMessageDelivered(watch, messageDeliveredChan, []common.Address{vm.address})
	if err != nil {
		return err
	}

	dispAssChan := make(chan *chainlauncher.ArbitrumVMPendingDisputableAssertion)
	dispAssSub, err := vm.ArbitrumVM.WatchPendingDisputableAssertion(watch, dispAssChan)
	if err != nil {
		return err
	}

	confAssChan := make(chan *chainlauncher.ArbitrumVMConfirmedDisputableAssertion)
	confAssSub, err := vm.ArbitrumVM.WatchConfirmedDisputableAssertion(watch, confAssChan)
	if err != nil {
		return err
	}

	challengeInitiatedChan := make(chan *chainlauncher.ArbitrumVMInitiatedChallenge)
	challengeInitiatedSub, err := vm.ArbitrumVM.WatchInitiatedChallenge(watch, challengeInitiatedChan)
	if err != nil {
		return err
	}

	challengeBisectedChan := make(chan *challengemanager.ChallengeManagerBisectedAssertion)
	challengeBisectedSub, err := vm.Challenge.WatchBisectedAssertion(watch, challengeBisectedChan, []common.Address{vm.address})
	if err != nil {
		return err
	}

	challengeBisectedOtherChan := make(chan *challengemanager.ChallengeManagerBisectedAssertionOther)
	challengeBisectedOtherSub, err := vm.Challenge.WatchBisectedAssertionOther(watch, challengeBisectedOtherChan, []common.Address{vm.address})
	if err != nil {
		return err
	}

	challengeContinuedChan := make(chan *challengemanager.ChallengeManagerContinuedChallenge)
	challengeContinuedSub, err := vm.Challenge.WatchContinuedChallenge(watch, challengeContinuedChan, []common.Address{vm.address})
	if err != nil {
		return err
	}

	challengeTimedOutChan := make(chan *challengemanager.ChallengeManagerTimedOutChallenge)
	challengeTimedOutSub, err := vm.Challenge.WatchTimedOutChallenge(watch, challengeTimedOutChan, []common.Address{vm.address})
	if err != nil {
		return err
	}

	oneStepProofChan := make(chan *challengemanager.ChallengeManagerOneStepProofCompleted)
	oneStepProofSub, err := vm.Challenge.WatchOneStepProofCompleted(watch, oneStepProofChan, []common.Address{vm.address})
	if err != nil {
		return err
	}

	go func() {
		defer headersSub.Unsubscribe()
		defer messageDeliveredSub.Unsubscribe()
		defer dispAssSub.Unsubscribe()
		defer confAssSub.Unsubscribe()
		defer challengeInitiatedSub.Unsubscribe()
		defer challengeBisectedSub.Unsubscribe()
		defer challengeBisectedOtherSub.Unsubscribe()
		defer challengeInitiatedSub.Unsubscribe()
		defer challengeContinuedSub.Unsubscribe()
		defer oneStepProofSub.Unsubscribe()

		for {
			select {
			case <-ctx.Done():
				break
			case header := <-headers:
				vm.OutChan <- Notification{
					Header: header,
					Event:  NewTimeEvent{},
				}
			case val := <-messageDeliveredChan:
				header, err := vm.Client.HeaderByHash(context.Background(), val.Raw.BlockHash)
				if err != nil {
					vm.ErrChan <- err
					return
				}
				rd := bytes.NewReader(val.Data)
				msgData, err := value.UnmarshalValue(rd)
				if err != nil {
					vm.ErrChan <- err
					return
				}

				messageHash := solsha3.SoliditySHA3(
					solsha3.Address(val.VmId),
					solsha3.Bytes32(msgData.Hash()),
					solsha3.Uint256(val.Value),
					val.TokenType[:],
				)
				msgHashInt := new(big.Int).SetBytes(messageHash[:])

				msgVal, _ := value.NewTupleFromSlice([]value.Value{
					msgData,
					value.NewIntValue(new(big.Int).SetUint64(header.Time)),
					value.NewIntValue(header.Number),
					value.NewIntValue(msgHashInt),
				})

				msg := protocol.NewSimpleMessage(msgVal, val.TokenType, val.Value, val.Sender)
				vm.OutChan <- Notification{
					Header: header,
					VMID:   val.VmId,
					Event: MessageDeliveredEvent{
						Msg: msg,
					},
					TxHash: val.Raw.TxHash,
				}
			case val := <-dispAssChan:
				header, err := vm.Client.HeaderByHash(ctx, val.Raw.BlockHash)
				if err != nil {
					vm.ErrChan <- err
					return
				}

				tokenTracker := protocol.NewTokenTrackerFromLists(val.TokenTypes, val.BeforeBalances)
				precondition := protocol.NewPrecondition(
					val.BeforeHash,
					val.TimeBounds,
					tokenTracker,
					value.NewHashOnlyValue(val.BeforeInbox, 1),
				)
				vm.OutChan <- Notification{
					Header: header,
					VMID:   vm.address,
					Event: PendingDisputableAssertionEvent{
						Precondition:  precondition,
						AssertionHash: val.AssertionHash,
						NumSteps:      val.NumSteps,
						Asserter:      val.Asserter,
					},
					TxHash: val.Raw.TxHash,
				}
			case val := <-confAssChan:
				header, err := vm.Client.HeaderByHash(ctx, val.Raw.BlockHash)
				if err != nil {
					vm.ErrChan <- err
					return
				}
				vm.OutChan <- Notification{
					Header: header,
					VMID:   vm.address,
					Event: ConfirmedDisputableAssertEvent{
						val.Raw.TxHash,
						val.LogsAccHash,
					},
					TxHash: val.Raw.TxHash,
				}
			case val := <-challengeInitiatedChan:
				header, err := vm.Client.HeaderByHash(ctx, val.Raw.BlockHash)
				if err != nil {
					vm.ErrChan <- err
					return
				}
				vm.OutChan <- Notification{
					Header: header,
					VMID:   vm.address,
					Event: InitiateChallengeEvent{
						Challenger: val.Challenger,
					},
					TxHash: val.Raw.TxHash,
				}
			case val := <-challengeBisectedChan:
				header, err := vm.Client.HeaderByHash(ctx, val.Raw.BlockHash)
				if err != nil {
					vm.ErrChan <- err
					return
				}
				vm.OutChan <- Notification{
					Header: header,
					VMID:   vm.address,
					Event: BisectionEvent{
						BisectionHashes: val.BisectionHashes,
					},
					TxHash: val.Raw.TxHash,
				}
			case val := <-challengeBisectedOtherChan:
				header, err := vm.Client.HeaderByHash(ctx, val.Raw.BlockHash)
				if err != nil {
					vm.ErrChan <- err
					return
				}
				vm.OutChan <- Notification{
					Header: header,
					VMID:   vm.address,
					Event: BisectionEvent{
						BisectionHashes:   val.BisectionHashes,
						SpentOutputValues: val.PrevOutputValues,
					},
					TxHash: val.Raw.TxHash,
				}
			case val := <-challengeTimedOutChan:
				header, err := vm.Client.HeaderByHash(ctx, val.Raw.BlockHash)
				if err != nil {
					vm.ErrChan <- err
					return
				}
				if val.ChallengerWrong {
					vm.OutChan <- Notification{
						Header: header,
						VMID:   vm.address,
						Event:  AsserterTimeoutEvent{},
						TxHash: val.Raw.TxHash,
					}
				} else {
					vm.OutChan <- Notification{
						Header: header,
						VMID:   vm.address,
						Event:  ChallengerTimeoutEvent{},
						TxHash: val.Raw.TxHash,
					}
				}
			case val := <-challengeContinuedChan:
				header, err := vm.Client.HeaderByHash(ctx, val.Raw.BlockHash)
				if err != nil {
					vm.ErrChan <- err
					return
				}
				vm.OutChan <- Notification{
					Header: header,
					VMID:   vm.address,
					Event: ContinueChallengeEvent{
						ChallengedAssertion: uint16(val.AssertionIndex.Uint64()),
					},
					TxHash: val.Raw.TxHash,
				}
			case val := <-oneStepProofChan:
				header, err := vm.Client.HeaderByHash(ctx, val.Raw.BlockHash)
				if err != nil {
					vm.ErrChan <- err
					return
				}
				vm.OutChan <- Notification{
					Header: header,
					VMID:   vm.address,
					Event:  OneStepProofEvent{},
					TxHash: val.Raw.TxHash,
				}
			case err := <-headersSub.Err():
				vm.ErrChan <- err
				return
			case err := <-messageDeliveredSub.Err():
				vm.ErrChan <- err
				return
			case err := <-dispAssSub.Err():
				vm.ErrChan <- err
				return
			case err := <-confAssSub.Err():
				vm.ErrChan <- err
				return
			case err := <-challengeInitiatedSub.Err():
				vm.ErrChan <- err
				return
			case err := <-challengeBisectedSub.Err():
				vm.ErrChan <- err
				return
			case err := <-challengeBisectedOtherSub.Err():
				vm.ErrChan <- err
				return
			case err := <-challengeContinuedSub.Err():
				vm.ErrChan <- err
				return
			case err := <-challengeTimedOutSub.Err():
				vm.ErrChan <- err
				return
			case err := <-oneStepProofSub.Err():
				vm.ErrChan <- err
				return
			}
		}
	}()
	return nil
}

func (vm *ArbitrumVM) PendingDisputableAssert(
	auth *bind.TransactOpts,
	precondition *protocol.Precondition,
	assertion *protocol.Assertion,
) (*types.Receipt, error) {
	balance := protocol.NewTokenTrackerFromMessages(assertion.OutMsgs)
	tokenTypes, beforeBalances := balance.GetTypesAndAmounts()
	tx, err := vm.ArbitrumVM.PendingDisputableAssert(
		auth,
		precondition.BeforeHash,
		precondition.TimeBounds,
		precondition.BeforeInbox.Hash(),
		tokenTypes,
		beforeBalances,
		assertion.NumSteps,
		assertion.Stub().Hash(),
	)
	if err != nil {
		return nil, err
	}
	return waitForReceipt(auth.Context, vm.Client, tx.Hash(), "PendingDisputableAssert")
}

func (vm *ArbitrumVM) ConfirmDisputableAsserted(
	auth *bind.TransactOpts,
	precondition *protocol.Precondition,
	assertion *protocol.Assertion,
) (*types.Receipt, error) {
	tokenNums, amounts, destinations, tokenTypes := hashing.SplitMessages(assertion.OutMsgs)

	var messageData bytes.Buffer
	for _, msg := range assertion.OutMsgs {
		err := value.MarshalValue(msg.Data, &messageData)
		if err != nil {
			return nil, err
		}
	}

	tx, err := vm.ArbitrumVM.ConfirmDisputableAsserted(
		auth,
		precondition.Hash(),
		assertion.AfterHash,
		assertion.NumSteps,
		tokenTypes,
		messageData.Bytes(),
		tokenNums,
		amounts,
		destinations,
		assertion.LogsHash(),
	)
	if err != nil {
		return nil, err
	}
	return waitForReceipt(auth.Context, vm.Client, tx.Hash(), "ConfirmDisputableAsserted")
}

func (vm *ArbitrumVM) InitiateChallenge(
	auth *bind.TransactOpts,
	precondition *protocol.Precondition,
	assertionHash [32]byte,
	numSteps uint32,
) (*types.Receipt, error) {
	tx, err := vm.ArbitrumVM.InitiateChallenge(
		auth,
		precondition.Hash(),
		assertionHash,
		numSteps,
	)
	if err != nil {
		return nil, err
	}
	return waitForReceipt(auth.Context, vm.Client, tx.Hash(), "InitiateChallenge")
}

func (vm *ArbitrumVM) BisectAssertionFirst(
	auth *bind.TransactOpts,
	assertion *protocol.AssertionStub,
	precondition *protocol.Precondition,
	bisections []*protocol.AssertionStub,
) (*types.Receipt, error) {
	bisectionHashes := make([][32]byte, 0, len(bisections))
	for _, assertion := range bisections {
		bisectionHashes = append(bisectionHashes, assertion.Hash())
	}
	tx, err := vm.Challenge.BisectAssertionFirst(
		auth,
		vm.address,
		assertion.NumSteps,
		precondition.Hash(),
		assertion.Hash(),
		bisectionHashes,
	)
	if err != nil {
		return nil, err
	}
	return waitForReceipt(auth.Context, vm.Client, tx.Hash(), "BisectAssertion")
}

func (vm *ArbitrumVM) BisectAssertionOther(
	auth *bind.TransactOpts,
	firstAssertion *protocol.AssertionStub,
	secondAssertion *protocol.AssertionStub,
	precondition *protocol.Precondition,
	bisections []*protocol.AssertionStub,
) (*types.Receipt, error) {
	bisectionHashes := make([][32]byte, 0, len(bisections))
	for _, assertion := range bisections {
		bisectionHashes = append(bisectionHashes, assertion.Hash())
	}
	tokenTypes, amounts := precondition.BeforeBalance.GetTypesAndAmounts()
	tx, err := vm.Challenge.BisectAssertionOther(
		auth,
		vm.address,
		[10][32]byte{
			precondition.BeforeHash,
			precondition.BeforeInbox.Hash(),
			firstAssertion.FirstMessageHash,
			firstAssertion.FirstLogHash,
			firstAssertion.AfterHash,
			firstAssertion.LastMessageHash,
			firstAssertion.LastLogHash,
			secondAssertion.AfterHash,
			secondAssertion.LastMessageHash,
			secondAssertion.LastLogHash,
		},
		precondition.TimeBounds,
		tokenTypes,
		amounts,
		firstAssertion.NumSteps,
		firstAssertion.TotalVals,
		secondAssertion.NumSteps,
		secondAssertion.TotalVals,
		bisectionHashes,
	)
	if err != nil {
		return nil, err
	}
	return waitForReceipt(auth.Context, vm.Client, tx.Hash(), "BisectAssertion")
}

func (vm *ArbitrumVM) ContinueChallenge(
	auth *bind.TransactOpts,
	assertionToChallenge uint16,
	precondition *protocol.Precondition,
	totalSteps uint32,
	assertion [32]byte,
	bisections [][32]byte,
) (*types.Receipt, error) {
	tree := buildBisectionTree(precondition, totalSteps, assertion, bisections)
	tx, err := vm.Challenge.ContinueChallenge(
		auth,
		vm.address,
		big.NewInt(int64(assertionToChallenge)),
		tree.GetProofFlat(int(assertionToChallenge)),
		tree.GetRoot(),
		tree.GetNode(int(assertionToChallenge)),
	)
	if err != nil {
		return nil, err
	}
	return waitForReceipt(auth.Context, vm.Client, tx.Hash(), "ContinueChallenge")
}

func (vm *ArbitrumVM) OneStepProofFirst(
	auth *bind.TransactOpts,
	assertion *protocol.AssertionStub,
	precondition *protocol.Precondition,
	proof []byte,
) (*types.Receipt, error) {
	tokenTypes, amounts := precondition.BeforeBalance.GetTypesAndAmounts()
	tx, err := vm.Challenge.OneStepProofFirst(
		auth,
		vm.address,
		[2][32]byte{precondition.BeforeHash, precondition.BeforeInbox.Hash()},
		precondition.TimeBounds,
		tokenTypes,
		amounts,
		[5][32]byte{
			assertion.AfterHash,
			assertion.FirstMessageHash,
			assertion.LastMessageHash,
			assertion.FirstLogHash,
			assertion.LastLogHash,
		},
		assertion.TotalVals,
		proof,
	)
	if err != nil {
		return nil, err
	}
	return waitForReceipt(auth.Context, vm.Client, tx.Hash(), "OneStepProof")
}

func (vm *ArbitrumVM) OneStepProofOther(
	auth *bind.TransactOpts,
	firstAssertion *protocol.AssertionStub,
	secondAssertion *protocol.AssertionStub,
	precondition *protocol.Precondition,
	proof []byte,
) (*types.Receipt, error) {
	tokenTypes, amounts := precondition.BeforeBalance.GetTypesAndAmounts()
	tx, err := vm.Challenge.OneStepProofOther(
		auth,
		vm.address,
		[10][32]byte{
			precondition.BeforeHash,
			precondition.BeforeInbox.Hash(),
			firstAssertion.FirstMessageHash,
			firstAssertion.FirstLogHash,
			firstAssertion.AfterHash,
			firstAssertion.LastMessageHash,
			firstAssertion.LastLogHash,
			secondAssertion.AfterHash,
			secondAssertion.LastMessageHash,
			secondAssertion.LastLogHash,
		},
		precondition.TimeBounds,
		tokenTypes,
		amounts,
		firstAssertion.NumSteps,
		firstAssertion.TotalVals,
		secondAssertion.NumSteps,
		secondAssertion.TotalVals,
		proof,
	)
	if err != nil {
		return nil, err
	}
	return waitForReceipt(auth.Context, vm.Client, tx.Hash(), "OneStepProof")
}

func (vm *ArbitrumVM) AsserterTimedOutChallenge(
	auth *bind.TransactOpts,
) (*types.Receipt, error) {
	tx, err := vm.Challenge.AsserterTimedOut(
		auth,
		vm.address,
	)
	if err != nil {
		return nil, err
	}
	return waitForReceipt(auth.Context, vm.Client, tx.Hash(), "AsserterTimedOut")
}

func (vm *ArbitrumVM) ChallengerTimedOutChallenge(
	auth *bind.TransactOpts,
) (*types.Receipt, error) {
	tx, err := vm.Challenge.ChallengerTimedOut(
		auth,
		vm.address,
	)
	if err != nil {
		return nil, err
	}
	return waitForReceipt(auth.Context, vm.Client, tx.Hash(), "ChallengerTimedOut")
}

func (vm *ArbitrumVM) CurrentDeposit(
	auth *bind.CallOpts,
	address common.Address,
) (*big.Int, error) {
	return vm.ArbitrumVM.CurrentDeposit(auth, address)
}

func (vm *ArbitrumVM) EscrowRequired(
	auth *bind.CallOpts,
) (*big.Int, error) {
	return vm.ArbitrumVM.EscrowRequired(auth)
}

func (vm *ArbitrumVM) IsEnabled(
	auth *bind.CallOpts,
) (bool, error) {
	status, err := vm.ArbitrumVM.GetState(auth)
	return status != 0, err
}

func (vm *ArbitrumVM) IsInChallenge(
	auth *bind.CallOpts,
) (bool, error) {
	vmState, err := vm.ArbitrumVM.Vm(auth)
	if err != nil {
		return false, err
	}
	return vmState.InChallenge, nil
}

func (vm *ArbitrumVM) IsPendingUnanimous(
	auth *bind.CallOpts,
) (bool, error) {
	status, err := vm.ArbitrumVM.GetState(auth)
	return status == 3, err
}

func (vm *ArbitrumVM) VerifyVM(
	auth *bind.CallOpts,
	config *valmessage.VMConfiguration,
	machine [32]byte,
) error {
	//code, err := vm.contract.Client.CodeAt(auth.Context, vm.address, nil)
	// Verify that VM has correct code
	vmInfo, err := vm.ArbitrumVM.Vm(auth)
	if err != nil {
		return err
	}

	if vmInfo.MachineHash != machine {
		return errors.New("VM has different machine hash")
	}

	if config.GracePeriod != uint64(vmInfo.GracePeriod) {
		return errors.New("VM has different grace period")
	}

	if value.NewBigIntFromBuf(config.EscrowRequired).Cmp(vmInfo.EscrowRequired) != 0 {
		return errors.New("VM has different escrow required")
	}

	if config.MaxExecutionStepCount != vmInfo.MaxExecutionSteps {
		return errors.New("VM has different mxa steps")
	}

	owner, err := vm.ArbitrumVM.Owner(auth)
	if err != nil {
		return err
	}
	if protocol.NewAddressFromBuf(config.Owner) != owner {
		return errors.New("VM has different owner")
	}
	return nil
}

func buildBisectionTree(
	precondition *protocol.Precondition,
	totalSteps uint32,
	assertion [32]byte,
	bisections [][32]byte,
) *MerkleTree {
	stepCounts := machine.BisectionStepCounts(uint32(len(bisections)), totalSteps)
	bisectionHashes := make([][32]byte, 0, len(bisections)+1)
	for i := range bisections {
		var bisectionBytes []byte
		if i == 0 {
			bisectionBytes = solsha3.SoliditySHA3(
				solsha3.Bytes32(precondition.Hash()),
				solsha3.Bytes32(bisections[i]),
				solsha3.Uint32(stepCounts[i]),
			)
		} else {
			bisectionBytes = solsha3.SoliditySHA3(
				solsha3.Bytes32(precondition.Hash()),
				solsha3.Bytes32(bisections[i-1]),
				solsha3.Bytes32(bisections[i]),
				solsha3.Uint32(stepCounts[i]),
			)
		}
		bisectionHash := [32]byte{}
		copy(bisectionHash[:], bisectionBytes)
		bisectionHashes = append(bisectionHashes, bisectionHash)
	}
	bisectionHash := [32]byte{}
	copy(bisectionHash[:], solsha3.SoliditySHA3(
		solsha3.Bytes32(precondition.Hash()),
		solsha3.Bytes32(bisections[len(bisections)-1]),
		solsha3.Bytes32(assertion),
		solsha3.Uint32(stepCounts[len(bisections)]),
	))
	return NewMerkleTree(bisectionHashes)
}