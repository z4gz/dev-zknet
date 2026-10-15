// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {WorkerSpecifier} from "../src/WorkerSpecifier.sol";
import {CHEATS, Cheats} from "./utils/Cheats.sol";

/// @dev What a harness and an indexer observe of the specifier, beyond the upstream unit tests: the exact
/// WorkerUpdated / OwnershipTransferred logs, resolver order and kinds preserved verbatim, and updates that replace
/// (not append to) the resolver list.
contract WorkerSpecifierEventsTest {
    bytes32 internal constant HASH_A = keccak256("zknet bundle a");
    bytes32 internal constant HASH_B = keccak256("zknet bundle b");
    address internal constant STRANGER = address(0xBEEF);

    bytes32 internal constant WORKER_UPDATED = keccak256("WorkerUpdated(bytes32,string[])");
    bytes32 internal constant OWNERSHIP_TRANSFERRED = keccak256("OwnershipTransferred(address,address)");

    function test_ConstructorEmitsOwnershipThenWorkerUpdated() public {
        string[] memory r = _resolvers3();
        CHEATS.recordLogs();
        WorkerSpecifier spec = new WorkerSpecifier(HASH_A, r);
        Cheats.Log[] memory logs = CHEATS.getRecordedLogs();

        require(logs.length == 2, "two logs");
        require(logs[0].emitter == address(spec) && logs[1].emitter == address(spec), "emitter");
        require(logs[0].topics.length == 3 && logs[0].topics[0] == OWNERSHIP_TRANSFERRED, "ownership topic");
        require(logs[0].topics[1] == bytes32(0), "from zero");
        require(logs[0].topics[2] == bytes32(uint256(uint160(address(this)))), "to deployer");
        _requireWorkerUpdated(logs[1], HASH_A, r);
    }

    function test_SetWorkerEmitsWorkerUpdatedWithTheNewValues() public {
        WorkerSpecifier spec = new WorkerSpecifier(HASH_A, _resolvers3());
        string[] memory next = new string[](1);
        next[0] = "https://mirror.test/keccak/aa/bb";

        CHEATS.recordLogs();
        spec.setWorker(HASH_B, next);
        Cheats.Log[] memory logs = CHEATS.getRecordedLogs();

        require(logs.length == 1, "one log");
        _requireWorkerUpdated(logs[0], HASH_B, next);
        string[] memory got = spec.workerResolvers();
        require(got.length == 1 && _eq(got[0], next[0]), "resolvers replaced, not appended");
    }

    function test_ResolversRoundTripVerbatimAndInOrder() public {
        string[] memory r = _resolvers3();
        WorkerSpecifier spec = new WorkerSpecifier(HASH_A, r);
        string[] memory got = spec.workerResolvers();
        require(keccak256(abi.encode(got)) == keccak256(abi.encode(r)), "verbatim, in order");
    }

    function test_EmptyResolverListIsAllowed() public {
        // §4: resolvers are advisory; a harness may obtain the bytes by any means.
        WorkerSpecifier spec = new WorkerSpecifier(HASH_A, new string[](0));
        require(spec.workerHash() == HASH_A, "hash");
        require(spec.workerResolvers().length == 0, "no resolvers");
    }

    function test_NonOwnerUpdateRevertsAndEmitsNothing() public {
        WorkerSpecifier spec = new WorkerSpecifier(HASH_A, _resolvers3());
        CHEATS.recordLogs();
        CHEATS.prank(STRANGER);
        CHEATS.expectRevert(WorkerSpecifier.NotOwner.selector);
        spec.setWorker(HASH_B, new string[](0));
        require(CHEATS.getRecordedLogs().length == 0, "no logs");
        require(spec.workerHash() == HASH_A, "unchanged");
    }

    function testFuzz_WorkerHashFollowsTheLatestUpdate(bytes32 first, bytes32 second) public {
        WorkerSpecifier spec = new WorkerSpecifier(first, new string[](0));
        require(spec.workerHash() == first, "first");
        spec.setWorker(second, new string[](0));
        require(spec.workerHash() == second, "second");
    }

    function _requireWorkerUpdated(Cheats.Log memory log, bytes32 hash, string[] memory resolvers) private pure {
        require(log.topics.length == 1 && log.topics[0] == WORKER_UPDATED, "WorkerUpdated topic");
        (bytes32 gotHash, string[] memory gotResolvers) = abi.decode(log.data, (bytes32, string[]));
        require(gotHash == hash, "event hash");
        require(keccak256(abi.encode(gotResolvers)) == keccak256(abi.encode(resolvers)), "event resolvers");
    }

    /// One entry of each §4.1 kind a deployed specifier can usefully carry, plus the local development form.
    function _resolvers3() private pure returns (string[] memory r) {
        r = new string[](3);
        r[0] = "http://127.0.0.1:8546/keccak/19/4f04bde4925f6bbb0bd8bdfceca7251125eaa0664ce3c0c25dce2a1545338d";
        r[1] =
            "https://raw.githubusercontent.com/example/worker/keccak/19/4f04bde4925f6bbb0bd8bdfceca7251125eaa0664ce3c0c25dce2a1545338d";
        r[2] =
            "kps:203.0.113.10:15005:uEiBfU3pQgjdx0UAXVjX5E73SDaz3GOV3554Ff7Yze-8_aQ/keccak/19/4f04bde4925f6bbb0bd8bdfceca7251125eaa0664ce3c0c25dce2a1545338d";
    }

    function _eq(string memory a, string memory b) private pure returns (bool) {
        return keccak256(bytes(a)) == keccak256(bytes(b));
    }
}
