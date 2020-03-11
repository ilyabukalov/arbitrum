/*
 * Copyright 2019-2020, Offchain Labs, Inc.
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

package gobridge

import (
	"context"
	"github.com/offchainlabs/arbitrum/packages/arb-util/hashing"
	"github.com/offchainlabs/arbitrum/packages/arb-util/value"
	"github.com/offchainlabs/arbitrum/packages/arb-validator-core/arbbridge"
	//"github.com/offchainlabs/arbitrum/packages/arb-validator-core/arbbridge"
	"github.com/offchainlabs/arbitrum/packages/arb-validator-core/message"
	"math/big"

	"github.com/offchainlabs/arbitrum/packages/arb-util/common"
)

type inbox struct {
	value common.Hash
	count *big.Int
}

type globalInbox struct {
	ethData         *goEthdata
	inbox           map[common.Address]*inbox
	contractAddress common.Address
}

func (ib *inbox) addMessageToInbox(msg common.Hash) {
	ib.value = hashing.SoliditySHA3(
		hashing.Bytes32(ib.value),
		hashing.Bytes32(msg),
	)
	ib.count = new(big.Int).Add(ib.count, big.NewInt(1))
}

func newGlobalInbox(address common.Address, client *GoArbAuthClient) (*globalInbox, error) {
	return &globalInbox{
		ethData:         client.goEthdata,
		inbox:           make(map[common.Address]*inbox),
		contractAddress: client.getNextAddress(),
	}, nil
}

func newInbox(address common.Address, client *GoArbAuthClient) (*inbox, error) {
	client.globalInbox.inbox[address] = &inbox{
		value: value.NewEmptyTuple().Hash(),
		count: big.NewInt(0),
	}
	return client.globalInbox.inbox[address], nil
}

func (con *globalInbox) SendTransactionMessage(ctx context.Context, data []byte, vmAddress common.Address, contactAddress common.Address, amount *big.Int, seqNumber *big.Int) error {
	con.ethData.goEthMutex.Lock()
	defer con.ethData.goEthMutex.Unlock()
	msgHash := hashing.SoliditySHA3(
		hashing.Uint8(0), // TRANSACTION_MSG
		hashing.Address(vmAddress),
		hashing.Address(contactAddress),
		hashing.Uint256(seqNumber),
		hashing.Uint256(amount),
		data,
		hashing.TimeBlocks(con.ethData.getCurrentBlock().Height),
	)

	con.deliverMessage(vmAddress, msgHash)
	msg := message.DeliveredTransaction{
		Transaction: message.Transaction{
			Chain:       con.contractAddress,
			To:          contactAddress,
			From:        vmAddress,
			SequenceNum: seqNumber,
			Value:       amount,
			Data:        data,
		},
		BlockNum: con.ethData.getCurrentBlock().Height,
	}

	con.ethData.pubMsg(con.contractAddress, arbbridge.MessageDeliveredEvent{
		ChainInfo: arbbridge.ChainInfo{
			BlockId: con.ethData.getCurrentBlock(),
		},
		Message: msg,
	})

	return nil
}

func (con *globalInbox) DeliverTransactionBatch(
	ctx context.Context,
	chain common.Address,
	transactions []message.Transaction,
	signatures [][65]byte,
) error {
	con.ethData.goEthMutex.Lock()
	defer con.ethData.goEthMutex.Unlock()
	for _, tx := range transactions {
		deliveredTransaction := message.DeliveredTransaction{
			Transaction: tx,
			BlockNum:    con.ethData.getCurrentBlock().Height,
		}
		msgHash := deliveredTransaction.CommitmentHash()

		con.inbox[chain].addMessageToInbox(msgHash)

		con.ethData.pubMsg(con.contractAddress, arbbridge.MessageDeliveredEvent{
			ChainInfo: arbbridge.ChainInfo{
				BlockId: con.ethData.getCurrentBlock(),
			},
			Message: message.DeliveredTransaction{
				Transaction: message.Transaction{
					Chain:       chain,
					To:          tx.To,
					From:        tx.From,
					SequenceNum: tx.SequenceNum,
					Value:       tx.Value,
					Data:        tx.Data,
				},
				BlockNum: con.ethData.getCurrentBlock().Height,
			},
		})
	}
	return nil
}

func (con *globalInbox) DepositEthMessage(
	ctx context.Context,
	vmAddress common.Address,
	destination common.Address,
	value *big.Int,
) error {
	con.ethData.goEthMutex.Lock()
	defer con.ethData.goEthMutex.Unlock()
	// depositEth
	depositEth(con.ethData, vmAddress, destination, value)

	//deliverEthMessage
	msgNum := new(big.Int).Add(con.inbox[vmAddress].count, big.NewInt(1))

	msg := message.DeliveredEth{
		Eth: message.Eth{
			To:    destination,
			From:  vmAddress,
			Value: value,
		},
		BlockNum:   con.ethData.getCurrentBlock().Height,
		MessageNum: msgNum,
	}
	msgHash := msg.CommitmentHash()

	con.inbox[vmAddress].addMessageToInbox(msgHash)

	con.ethData.pubMsg(con.contractAddress, arbbridge.MessageDeliveredEvent{
		ChainInfo: arbbridge.ChainInfo{
			BlockId: con.ethData.getCurrentBlock(),
		},
		Message: msg,
	})
	return nil
}

func (con *globalInbox) DepositERC20Message(
	ctx context.Context,
	vmAddress common.Address,
	tokenAddress common.Address,
	destination common.Address,
	value *big.Int,
) error {
	con.ethData.goEthMutex.Lock()
	defer con.ethData.goEthMutex.Unlock()
	// deposit ERC20
	depositERC20(con.ethData, destination, vmAddress, value)

	// send message
	msgNum := new(big.Int).Add(con.inbox[vmAddress].count, big.NewInt(1))
	msg := message.DeliveredERC20{
		ERC20: message.ERC20{
			To:           destination,
			From:         vmAddress,
			TokenAddress: tokenAddress,
			Value:        value,
		},
		BlockNum:   con.ethData.getCurrentBlock().Height,
		MessageNum: msgNum,
	}
	msgHash := msg.CommitmentHash()
	con.inbox[vmAddress].addMessageToInbox(msgHash)

	con.ethData.pubMsg(con.contractAddress, arbbridge.MessageDeliveredEvent{
		ChainInfo: arbbridge.ChainInfo{
			BlockId: con.ethData.getCurrentBlock(),
		},
		Message: msg,
	})
	return nil
}

func (con *globalInbox) DepositERC721Message(
	ctx context.Context,
	vmAddress common.Address,
	tokenAddress common.Address,
	destination common.Address,
	value *big.Int,
) error {
	con.ethData.goEthMutex.Lock()
	defer con.ethData.goEthMutex.Unlock()
	// deposit ERC721
	depositERC721(con.ethData, destination, vmAddress, value)

	// send message
	msgNum := new(big.Int).Add(con.inbox[vmAddress].count, big.NewInt(1))
	msg := message.DeliveredERC721{
		ERC721: message.ERC721{
			To:           tokenAddress,
			From:         vmAddress,
			TokenAddress: destination,
			Id:           value,
		},
		BlockNum:   con.ethData.getCurrentBlock().Height,
		MessageNum: msgNum,
	}
	msgHash := msg.CommitmentHash()
	con.inbox[vmAddress].addMessageToInbox(msgHash)

	con.ethData.pubMsg(con.contractAddress, arbbridge.MessageDeliveredEvent{
		ChainInfo: arbbridge.ChainInfo{
			BlockId: con.ethData.getCurrentBlock(),
		},
		Message: msg,
	})
	return nil
}

func (con *globalInbox) GetTokenBalance(
	ctx context.Context,
	user common.Address,
	tokenContract common.Address,
) (*big.Int, error) {
	con.ethData.goEthMutex.Lock()
	defer con.ethData.goEthMutex.Unlock()
	// getERC20Balance
	return con.ethData.ftWallets[user].ftList[tokenContract], nil
}

func (con *globalInbox) deliverMessage(address common.Address, msgHash common.Hash) {
	hash := hashing.SoliditySHA3(
		hashing.Bytes32(con.inbox[address].value),
		hashing.Bytes32(msgHash),
	)
	con.inbox[address].value = hash
	con.inbox[address].count = new(big.Int).Add(con.inbox[address].count, big.NewInt(1))
}
