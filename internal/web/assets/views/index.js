// views/index.js — the surfaces that plug into the views registry. Each
// module registers itself with registerView on import; app.js imports this
// file once, so adding a surface is one module here and one line below.
import './goals.js?v=__ASSET_V__'
import './stacks.js?v=__ASSET_V__'
import './ingest.js?v=__ASSET_V__'
import './bugs.js?v=__ASSET_V__'
import './doctor.js?v=__ASSET_V__'
import './agent.js?v=__ASSET_V__'
import './agentplugins.js?v=__ASSET_V__'
import './fleet.js?v=__ASSET_V__'
import './newcard.js?v=__ASSET_V__'
