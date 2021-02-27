/*
 * Copyright 2020, Offchain Labs, Inc.
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

#include "config.hpp"
#include "helper.hpp"

#include <data_storage/arbstorage.hpp>
#include <data_storage/storageresult.hpp>

#include <avm/inboxmessage.hpp>

#include <avm_values/vmValueParser.hpp>

#include <catch2/catch.hpp>
#include <nlohmann/json.hpp>

TEST_CASE("ArbCore tests") {
    DBDeleter deleter;
    ValueCache value_cache{};

    ArbStorage storage(dbpath);
    REQUIRE(storage.initialize(arb_os_path).ok());
    auto arbCore = storage.getArbCore();
    REQUIRE(arbCore->startThread());

    std::vector<std::string> files = {
        "evm_direct_deploy_add", "evm_direct_deploy_and_call_add",
        "evm_test_arbsys", "evm_xcontract_call_with_constructors"};

    uint64_t logs_count = 0;

    for (const auto& filename : files) {
        INFO("Testing " << filename);
        auto test_file =
            std::string{arb_os_test_cases_path} + "/" + filename + ".aoslog";

        std::ifstream i(test_file);
        nlohmann::json j;
        i >> j;

        std::vector<Tuple> inbox_message_tuples;
        for (auto& json_message : j.at("inbox")) {
            auto tup = std::get<Tuple>(simple_value_from_json(json_message));
            inbox_message_tuples.push_back(std::move(tup));
        }

        std::vector<InboxMessage> inbox_messages;
        inbox_messages.reserve(inbox_message_tuples.size());
        for (const auto& msg : inbox_message_tuples) {
            inbox_messages.push_back(InboxMessage::fromTuple(msg));
        }

        std::vector<std::vector<unsigned char>> raw_messages;
        raw_messages.reserve(inbox_messages.size());
        for (const auto& msg : inbox_messages) {
            raw_messages.push_back(msg.serialize());
        }

        for (size_t k = 0; k < raw_messages.size(); ++k) {
            auto msg = extractInboxMessage(raw_messages[k]);
            auto msg_tup = msg.toTuple();
            REQUIRE(hash(msg_tup) == hash(inbox_message_tuples[k]));
        }

        auto logs_json = j.at("logs");
        std::vector<value> logs;
        for (auto& log_json : logs_json) {
            logs.push_back(simple_value_from_json(log_json));
        }

        auto sends_json = j.at("sends");
        std::vector<std::vector<uint8_t>> sends;
        for (auto& send_json : sends_json) {
            sends.push_back(send_from_json(send_json));
        }

        REQUIRE(arbCore->deliverMessages(raw_messages, 0, false));

        ArbCore::message_status_enum status;
        while (true) {
            status = arbCore->messagesStatus();
            if (status != ArbCore::MESSAGES_EMPTY &&
                status != ArbCore::MESSAGES_READY) {
                break;
            }
            std::this_thread::sleep_for(std::chrono::milliseconds(100));
        }
        REQUIRE(status == ArbCore::MESSAGES_SUCCESS);

        int tries = 0;
        while (true) {
            auto countRes = arbCore->messageEntryInsertedCount();
            REQUIRE(countRes.status.ok());
            if (countRes.data == inbox_messages.size()) {
                break;
            }
            std::this_thread::sleep_for(std::chrono::milliseconds(1000));
            tries++;
            REQUIRE(tries < 5);
        }

        auto accRes = arbCore->getInboxAcc(inbox_messages.size() - 1);
        REQUIRE(accRes.status.ok());
        REQUIRE(accRes.data != 0);

        while (!arbCore->machineIdle()) {
            auto err_str = arbCore->machineClearError();
            REQUIRE(!err_str.has_value());
            std::this_thread::sleep_for(std::chrono::milliseconds(1000));
        }

        auto producedLogCountRes = arbCore->logInsertedCount();
        REQUIRE(producedLogCountRes.status.ok());
        REQUIRE(producedLogCountRes.data == logs.size());
        auto logsRes =
            arbCore->getLogs(0, producedLogCountRes.data, value_cache);
        REQUIRE(logsRes.status.ok());
        REQUIRE(logsRes.data.size() == logs.size());
        for (size_t k = 0; k < logs.size(); ++k) {
            REQUIRE(logsRes.data[k] == logs[k]);
        }

        auto producedSendCountRes = arbCore->sendInsertedCount();
        REQUIRE(producedSendCountRes.status.ok());
        REQUIRE(producedSendCountRes.data == sends.size());
        auto sendsRes = arbCore->getSends(0, producedSendCountRes.data);
        REQUIRE(sendsRes.status.ok());
        REQUIRE(sendsRes.data.size() == sends.size());
        for (size_t k = 0; k < sends.size(); ++k) {
            REQUIRE(sendsRes.data[k] == sends[k]);
        }

        auto log_request_count = 1;
        REQUIRE(arbCore->logsCursorRequest(0, log_request_count));
        while (true) {
            uint256_t first_deleted_log_index;
            auto deleted =
                arbCore->logsCursorGetDeletedLogs(0, first_deleted_log_index);
            if (deleted.has_value()) {
                REQUIRE(first_deleted_log_index <= logs_count);
                REQUIRE(deleted->size() == logs_count);
                logs_count -= deleted->size();
            }
            uint256_t first_log_index;
            auto result = arbCore->logsCursorGetLogs(0, first_log_index);
            REQUIRE(!arbCore->logsCursorCheckError(0));
            if (result.has_value()) {
                REQUIRE(first_log_index == logs_count);
                REQUIRE(result->size() <= logs.size() - logs_count);
                for (uint64_t k = 0; k < result->size(); ++k) {
                    REQUIRE(result->at(k) == logs[logs_count + k]);
                }
                logs_count += result->size();
                REQUIRE(arbCore->logsCursorConfirmReceived(0));
                break;
            }
            std::this_thread::sleep_for(std::chrono::milliseconds(1000));
        }
        REQUIRE(logs_count == logs.size());

        auto cursor = arbCore->getExecutionCursor(0, value_cache);
        REQUIRE(cursor.status.ok());
        REQUIRE(cursor.data->arb_gas_used == 0);

        auto advanceStatus = arbCore->advanceExecutionCursor(
            *cursor.data, 100, false, value_cache);
        REQUIRE(advanceStatus.ok());
        REQUIRE(cursor.data->arb_gas_used > 0);

        //        auto before_sideload = arbCore->getMachineForSideload(
        //            inbox_messages.back().block_number, value_cache);
        //        REQUIRE(before_sideload.status.ok());
        //        REQUIRE(before_sideload.data->machine_state.loadCurrentInstruction()
        //                    .op.opcode == OpCode::SIDELOAD);
    }
}