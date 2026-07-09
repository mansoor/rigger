// Minimal Node.js (Express) HTTP starter scaffolded by Rigger.
// Rigger builds this with templates/dockerfiles/nodejs/Dockerfile; the runtime runs
// `node dist/index.js`, and `npm run build` copies src → dist. Listens on $PORT.
const express = require('express')

const app = express()
const port = process.env.PORT || 3000

app.get('/health', (req, res) => res.json({ status: 'ok' }))
app.get('/', (req, res) => res.json({ app: 'rigger-node-starter', status: 'ok' }))

app.listen(port, () => console.log(`listening on :${port}`))
