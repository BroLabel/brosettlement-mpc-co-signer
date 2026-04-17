# brosettlement-mpc-co-signer

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

Runtime model:

- polls BroSettlement monolith for pending intents
- claims work over signed HTTP requests
- exchanges MPC frames via monolith message endpoints
- posts final MPC results back to monolith
