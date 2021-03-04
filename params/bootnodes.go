// Copyright 2015 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package params

// MainnetBootnodes are the enode URLs of the P2P bootstrap nodes running on
// the main Abeychain network.
var MainnetBootnodes = []string{
	"enode://cd99daa76de43e2b7a5806c3455d33012cd127bca9b2e271be3af5d78e402c153a77e1d408f708770fb390e597621407f963f1c444090c21f91e03e03caa2110@39.98.216.197:30313", // CN
	"enode://e95937d68263a59c95ac1199eecc450b3590624accaf1542c7e51d8dc3ca3bfa6d3f60785b021c408b4a9a67b2869da33237c75448ae29b70506164a2bfe6931@13.52.156.74:30313",  // US WEST
	"enode://9032cc37954363b4d2dd37a898959aadf213718ff1bdb146848fb8c9a5adfd31d543ca870a08a223b27da2309051d0ce41775fa6de9337ed519b64cfa85b5b0c@47.241.22.155:30313", // SG
	"enode://4c64220af42271b6a6ea5463e97a125fef86d0bbb077db7d669af9d020d8ccf8ef4b617e3b36bbb9c10096404ecc1a7e06bcec3210a2cdf49b2bce5a0e1c7eb5@8.209.88.41:30313",   // DE

	"enode://fb331ff6aded86b393d9de2f9c449d313b356af0c4c0b9500e0f6c51bcb4ed31ca45dc2ab64c6182d1876eb9e3fd073d488277a40a6d357bc6e63350a2e00ffc@101.132.183.35:30313", // CN
}

// TestnetBootnodes are the enode URLs of the P2P bootstrap nodes running on the
// Ropsten test network.
var TestnetBootnodes = []string{
	"enode://a395d2799c1e63307b9a5ecc44729e9ba2fb8fa6d64e362e8498ce9aba85b7b405755ad28bd662a9a48d941bbbfe18d29e0ea46105258110e2318fd6faab8c09@39.108.212.229:30313", // CN
	"enode://946dd380c75f756696e4183a3bba661f5a72dcd4af231189a966de7fb2b561ecdff7ef531ca090b6c22e32876368c5360a069d3ca709a107359d511c248eb0ac@52.167.174.211:30313", // US
	//"enode://50ac2f679890052610954f986157d434eeef8ed78eb3b2da62334f5822658ac96eff75e3d12fbf81289a6fb559cec03ba8179db02e98459b5eeb86b15d80cf69@138.128.216.58:30313", // EU
	"enode://cf04b2cfadb241358c8a08001e88244f79c1e12f8d3f57251c27b8cf5010dc7588c2de75fe9ea09eecfa3c7d16b2513290d3a3d1d1203324fef77e6fc231c707@47.74.185.172:30313", // SG
}

// DevnetBootnodes are the enode URLs of the P2P bootstrap nodes running on
// the dev Abeychain network.
var DevnetBootnodes = []string{
	"enode://ec1e13e3d0177196a55570dfc1c810b2ea05109cb310c4dc7397ae6f3109467ec0d13a5f28ebdfb553511d492a4892ffa3a8283ce69bc5f93fce079dbfbfa5f4@39.100.120.25:30310",
}

// DiscoveryV5Bootnodes are the enode URLs of the P2P bootstrap nodes for the
// experimental RLPx v5 topic-discovery network.
var DiscoveryV5Bootnodes = []string{
	"enode://ebb007b1efeea668d888157df36cf8fe49aa3f6fd63a0a67c45e4745dc081feea031f49de87fa8524ca29343a21a249d5f656e6daeda55cbe5800d973b75e061@39.98.171.41:30315",
	"enode://b5062c25dc78f8d2a8a216cebd23658f170a8f6595df16a63adfabbbc76b81b849569145a2629a65fe50bfd034e38821880f93697648991ba786021cb65fb2ec@39.98.43.179:30312",
}
