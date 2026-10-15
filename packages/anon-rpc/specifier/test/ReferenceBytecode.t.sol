// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {WorkerSpecifier} from "../src/WorkerSpecifier.sol";

/// @dev Guards the "vendored byte for byte" claim with the strongest available evidence: the contract compiled here
/// has the same runtime code, metadata hash included, as the reference WorkerSpecifier deployments on Ethereum
/// mainnet. Any edit to src/WorkerSpecifier.sol or to the compiler settings in foundry.toml changes this hash.
contract ReferenceBytecodeTest {
    /// keccak256 of the runtime code shared by EF's mainnet WorkerSpecifier deployments, e.g. the passthrough
    /// reference worker at 0x4fd77be300f31c5fe6ab266d35d27750a3478d27 (anon-rpc adopters.json5, kind "reference").
    bytes32 internal constant MAINNET_REFERENCE_CODEHASH =
        0xc4c54384a9c1f1201ef3711a7ed7ba20e078a432e85ecc4f8b14927587cc7b5f;

    /// keccak256 of EF's passthrough bundle, stored at fixtures/keccak/19/4f04....
    bytes32 internal constant PASSTHROUGH_HASH = 0x194f04bde4925f6bbb0bd8bdfceca7251125eaa0664ce3c0c25dce2a1545338d;

    function test_RuntimeCodeMatchesTheMainnetReference() public {
        string[] memory r = new string[](1);
        r[0] = "http://127.0.0.1:8546/keccak/19/4f04bde4925f6bbb0bd8bdfceca7251125eaa0664ce3c0c25dce2a1545338d";
        WorkerSpecifier spec = new WorkerSpecifier(PASSTHROUGH_HASH, r);
        require(address(spec).codehash == MAINNET_REFERENCE_CODEHASH, "runtime code differs from mainnet reference");
    }

    /// The contract has no immutables, so its runtime code cannot depend on constructor arguments.
    function testFuzz_RuntimeCodeIsIndependentOfConstructorArguments(bytes32 hash, string memory entry) public {
        string[] memory r = new string[](1);
        r[0] = entry;
        WorkerSpecifier spec = new WorkerSpecifier(hash, r);
        require(address(spec).codehash == MAINNET_REFERENCE_CODEHASH, "runtime code depends on arguments");
    }
}
