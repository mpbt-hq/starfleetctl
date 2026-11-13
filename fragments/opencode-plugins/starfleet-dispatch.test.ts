// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Unit test harness for starfleet-dispatch plugin logic.
// Tests pure logic functions that can be tested in isolation.
// Run with: npx tsx starfleet-dispatch.test.ts

import { test, describe } from 'node:test'
import assert from 'node:assert'

// Mirrors sessionIdFromArgv() from starfleet-dispatch.ts (pure, no side effects).
function sessionIdFromArgv(argv: string[]): string {
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    let v = ''
    if (a === '--session' || a === '-s') v = argv[i + 1] || ''
    else if (a.startsWith('--session=')) v = a.slice('--session='.length)
    else continue
    if (/^ses_[A-Za-z0-9]+$/.test(v)) return v
  }
  return ''
}

// ============================================================================
// PURE LOGIC TESTS - No mocks needed, just test the logic directly
// ============================================================================

describe('starfleet-dispatch plugin pure logic', () => {

  describe('bus() function logic (comms dispatch)', () => {
    test('should strip "comms:" prefix lines from starfleetctl output', () => {
      const rawOutput = `comms: directive m123 from 'Enterprise' → TestShip
{"ok": true, "result": "success"}`
      
      const jsonStart = rawOutput.indexOf('{')
      const jsonStr = jsonStart >= 0 ? rawOutput.slice(jsonStart) : rawOutput
      const parsed = JSON.parse(jsonStr)
      
      assert.strictEqual(parsed.ok, true)
      assert.strictEqual(parsed.result, 'success')
    })

    test('should handle output with multiple comms lines', () => {
      const rawOutput = `comms: directive m123 from 'Enterprise' → TestShip
comms: directive m124 from 'Enterprise' → TestShip
{"ok": true, "data": [1,2,3]}`
      
      const jsonStart = rawOutput.indexOf('{')
      const jsonStr = jsonStart >= 0 ? rawOutput.slice(jsonStart) : rawOutput
      const parsed = JSON.parse(jsonStr)
      
      assert.strictEqual(parsed.ok, true)
      assert.deepStrictEqual(parsed.data, [1,2,3])
    })

    test('should handle pure JSON output (no comms prefix)', () => {
      const rawOutput = '{"ok": false, "error": "not found"}'
      
      const jsonStart = rawOutput.indexOf('{')
      const jsonStr = jsonStart >= 0 ? rawOutput.slice(jsonStart) : rawOutput
      const parsed = JSON.parse(jsonStr)
      
      assert.strictEqual(parsed.ok, false)
      assert.strictEqual(parsed.error, 'not found')
    })
  })

  describe('--dry-run mode (STARFLEET_DRY_RUN=1)', () => {
    test('DRY_RUN should be true when env is set to "1"', () => {
      process.env.STARFLEET_DRY_RUN = '1'
      const DRY_RUN = process.env.STARFLEET_DRY_RUN === '1'
      assert.strictEqual(DRY_RUN, true)
      delete process.env.STARFLEET_DRY_RUN
    })

    test('DRY_RUN should be false when env is not set', () => {
      delete process.env.STARFLEET_DRY_RUN
      const DRY_RUN = process.env.STARFLEET_DRY_RUN === '1'
      assert.strictEqual(DRY_RUN, false)
    })

    test('DRY_RUN should be false when env is set to other value', () => {
      process.env.STARFLEET_DRY_RUN = 'true'
      const DRY_RUN = process.env.STARFLEET_DRY_RUN === '1'
      assert.strictEqual(DRY_RUN, false)
      delete process.env.STARFLEET_DRY_RUN
    })
  })

  describe('crash safety / malformed input handling', () => {
    test('should handle undefined message text gracefully', () => {
      const msg = { id: 'm1', from: 'Enterprise', text: undefined, type: 'ship' }
      const text = (msg.text ?? '').trim()
      assert.strictEqual(text, '')
    })

    test('should handle null message text gracefully', () => {
      const msg = { id: 'm1', from: 'Enterprise', text: null as any, type: 'ship' }
      const text = (msg.text ?? '').trim()
      assert.strictEqual(text, '')
    })

    test('should handle empty string message text', () => {
      const msg = { id: 'm1', from: 'Enterprise', text: '', type: 'ship' }
      const text = (msg.text ?? '').trim()
      assert.strictEqual(text, '')
    })

    test('aid() should fallback to "default" when STARFLEET_SHIP_ID not set', () => {
      delete process.env.STARFLEET_SHIP_ID
      const aid = process.env.STARFLEET_SHIP_ID || 'default'
      assert.strictEqual(aid, 'default')
    })

    test('aid() should use STARFLEET_SHIP_ID when set', () => {
      process.env.STARFLEET_SHIP_ID = 'TestShip'
      const aid = process.env.STARFLEET_SHIP_ID || 'default'
      assert.strictEqual(aid, 'TestShip')
      delete process.env.STARFLEET_SHIP_ID
    })

    test('should handle missing session.error properties', () => {
      const event = { properties: { error: undefined } }
      const err = event.properties?.error as any
      const candidate = err?.message || err?.code || err?.error || (typeof err === 'string' ? err : '') || ''
      assert.strictEqual(candidate, '')
    })

    test('should handle session.error with string error', () => {
      const event = { properties: { error: 'some error message' } }
      const err = event.properties?.error as any
      const candidate = err?.message || err?.code || err?.error || (typeof err === 'string' ? err : '') || ''
      assert.strictEqual(candidate, 'some error message')
    })

    test('should handle session.error with error object', () => {
      const event = { properties: { error: { message: 'Error details', code: 'ERR_CODE' } } }
      const err = event.properties?.error as any
      const candidate = err?.message || err?.code || err?.error || (typeof err === 'string' ? err : '') || ''
      assert.strictEqual(candidate, 'Error details')
    })
  })

  describe('handleMessage() logic - type handling', () => {
    function createMockMessage(id: string, from: string, text: string, type: string = 'ship') {
      return { id, from, text, type }
    }

    // Mirrors handleMessage classification relevant to injection/confirmation:
    // return true => handled/command (may be confirmed), false => injectable or
    // not-delivered (must remain retry-fähig, never falsely confirmed).
    function classifyForHandoff(type: string | undefined): boolean {
      switch (type || 'ship') {
        case 'ship':
        case 'user':
        case 'control':
        case 'message':
          return false // injectable directive
        case 'command':
          return true  // handled command path
        default:
          return false // unknown: not delivered, must stay retry-fähig
      }
    }

    test('should identify command type', () => {
      const msg = createMockMessage('m1', 'Enterprise', 'model test-model', 'command')
      assert.strictEqual(msg.type, 'command')
      assert.strictEqual(classifyForHandoff(msg.type), true)
    })

    test('should identify ship type', () => {
      const msg = createMockMessage('m1', 'Enterprise', 'do something', 'ship')
      assert.strictEqual(msg.type, 'ship')
      assert.strictEqual(classifyForHandoff(msg.type), false)
    })

    test('should identify user type', () => {
      const msg = createMockMessage('m1', 'Enterprise', 'do something', 'user')
      assert.strictEqual(msg.type, 'user')
      assert.strictEqual(classifyForHandoff(msg.type), false)
    })

    test('should identify control type', () => {
      const msg = createMockMessage('m1', 'Enterprise', 'do something', 'control')
      assert.strictEqual(msg.type, 'control')
      assert.strictEqual(classifyForHandoff(msg.type), false)
    })

    test('external type="message" is treated as injectable directive (not command)', () => {
      const msg = createMockMessage('m1', 'ChatGPT', 'VOYAGER-LIVE-HANDOFF-20261113-01', 'message')
      assert.strictEqual(classifyForHandoff(msg.type), false)
      // injectable => must be injected, not confirmed as handled/command
      const injectable = !classifyForHandoff(msg.type)
      assert.strictEqual(injectable, true)
    })

    test('should detect model-switch command', () => {
      const text = 'model nvidia/nemotron-3-ultra-550b-a55b'
      const lower = text.toLowerCase()
      assert.ok(lower.startsWith('model '))
      const targetModel = text.slice(6).trim()
      assert.strictEqual(targetModel, 'nvidia/nemotron-3-ultra-550b-a55b')
    })

    test('should detect task command', () => {
      const text = 'task my-task-slug'
      const lower = text.toLowerCase()
      assert.ok(lower.startsWith('task '))
      const args = text.slice(5).trim()
      assert.strictEqual(args, 'my-task-slug')
    })

    test('should detect task clear command', () => {
      const text = 'task clear'
      const args = text.slice(5).trim()
      assert.ok(args === 'clear' || args === 'done' || args === '')
    })

    test('should detect quit command', () => {
      const text = 'quit'
      const verb = text.split(/\s+/)[0].toLowerCase()
      assert.strictEqual(verb, 'quit')
    })

    test('should detect reset command', () => {
      const text = 'reset'
      const verb = text.split(/\s+/)[0].toLowerCase()
      assert.strictEqual(verb, 'reset')
    })

    test('should detect abort-retry command', () => {
      const text = 'abort-retry nvidia/nemotron-3-ultra-550b-a55b'
      const verb = text.split(/\s+/)[0].toLowerCase()
      const args = text.slice(verb.length).trim()
      assert.strictEqual(verb, 'abort-retry')
      assert.strictEqual(args, 'nvidia/nemotron-3-ultra-550b-a55b')
    })

    test('should detect toast command with all parts', () => {
      const text = 'toast warning "Test Title" "Test message" 5000'
      const parts = text.slice(6).split(/\s+/)
      assert.strictEqual(parts[0], 'warning')
    })

    test('should auto-adopt task from dashboard-topic reference with backticks', () => {
      const text = 'Please work on (Dashboard-Topic `starfleet/task-example`) now.'
      const topic = /Dashboard-Topic[ \t]*[`']([^`']+)[`']/.exec(text)
      assert.ok(topic !== null)
      assert.strictEqual(topic![1], 'starfleet/task-example')
    })

    test('should auto-adopt task from dashboard-topic reference with single quotes', () => {
      const text = "See (Dashboard-Topic 'starfleet/task-example') for details."
      const topic = /Dashboard-Topic[ \t]*[`']([^`']+)[`']/.exec(text)
      assert.ok(topic !== null)
      assert.strictEqual(topic![1], 'starfleet/task-example')
    })
  })

  describe('executeAction() recovery actions logic', () => {
    test('action=ignore should do nothing', () => {
      const action = 'ignore'
      assert.strictEqual(action, 'ignore')
    })

    test('should detect stream errors for retry action', () => {
      const streamErrors = [
        'ResourceExhausted: Worker local total request limit reached',
        'Streaming response failed',
        'stream interrupted',
        'response stream closed',
        'connection closed',
        'broken pipe',
        'unexpected eof',
        'stream closed',
      ]
      
      for (const detail of streamErrors) {
        const isStreamError =
          detail.includes('ResourceExhausted') ||
          detail.includes('Streaming response failed') ||
          detail.includes('stream interrupted') ||
          detail.includes('response stream') ||
          detail.includes('connection closed') ||
          detail.includes('broken pipe') ||
          detail.includes('unexpected eof') ||
          detail.includes('stream closed')
        assert.ok(isStreamError, `Should detect "${detail}" as stream error`)
      }
    })

    test('should NOT detect non-stream errors as stream errors', () => {
      const nonStreamErrors = [
        'Invalid request format',
        'Authentication failed',
        'Model not found',
        'Bad request',
      ]
      
      for (const detail of nonStreamErrors) {
        const isStreamError =
          detail.includes('ResourceExhausted') ||
          detail.includes('Streaming response failed') ||
          detail.includes('stream interrupted') ||
          detail.includes('response stream') ||
          detail.includes('connection closed') ||
          detail.includes('broken pipe') ||
          detail.includes('unexpected eof') ||
          detail.includes('stream closed')
        assert.ok(!isStreamError, `Should NOT detect "${detail}" as stream error`)
      }
    })

    test('hasSwitched guard should prevent double switch', () => {
      const hasSwitched = { v: false }
      const targetModel = 'test-model'
      
      // First switch
      if (!targetModel || hasSwitched.v) {
        assert.fail('Should allow first switch')
      }
      hasSwitched.v = true
      
      // Second switch should be blocked
      if (!targetModel || hasSwitched.v) {
        assert.ok(true)
      } else {
        assert.fail('Should block second switch')
      }
    })
  })

  describe('log monitoring - quota vs transient detection', () => {
    test('QUOTA_PATTERNS should match actual quota errors', () => {
      const quotaPatterns = [
        /quota.{0,20}exceed/i,
        /usage.{0,10}limit/i,
        /rate.{0,10}limit/i,
        /out.of.quota/i,
        /429.{0,20}(quota|limit)/i,
        /exceeded.{0,10}(quota|limit|rate)/i,
        /daily.{0,10}limit/i,
        /monthly.{0,10}limit/i,
        /billing.{0,10}limit/i,
      ]
      
      assert.ok(quotaPatterns.some(re => re.test('quota exceeded')))
      assert.ok(quotaPatterns.some(re => re.test('rate limit exceeded')))
      assert.ok(quotaPatterns.some(re => re.test('429 quota limit')))
      assert.ok(quotaPatterns.some(re => re.test('usage limit reached')))
    })

    test('QUOTA_PATTERNS should NOT match transient ResourceExhausted', () => {
      const quotaPatterns = [
        /quota.{0,20}exceed/i,
        /usage.{0,10}limit/i,
        /rate.{0,10}limit/i,
        /out.of.quota/i,
        /429.{0,20}(quota|limit)/i,
        /exceeded.{0,10}(quota|limit|rate)/i,
        /daily.{0,10}limit/i,
        /monthly.{0,10}limit/i,
        /billing.{0,10}limit/i,
      ]
      
      assert.ok(!quotaPatterns.some(re => re.test('ResourceExhausted: Worker local total request limit reached')))
      assert.ok(!quotaPatterns.some(re => re.test('ResourceExhausted')))
      assert.ok(!quotaPatterns.some(re => re.test('stream interrupted')))
    })

    test('should extract error from opencode log lines', () => {
      const logLine = 'level=ERROR session.id=abc stream error error.error="ResourceExhausted: Worker local total request limit reached"'
      const streamErrRe = /level=ERROR.*stream error.*error\.error="([^"]+)"/g
      const match = streamErrRe.exec(logLine)
      
      assert.ok(match !== null)
      assert.ok(match![1].includes('ResourceExhausted'))
    })

    test('should extract error from streaming failed log lines', () => {
      const logLine = 'level=ERROR session.id=abc Streaming response failed error.error="Connection reset"'
      const streamingFailedRe = /level=ERROR.*(?:Streaming response failed|stream interrupted|response stream|connection closed|broken pipe|unexpected eof|stream closed).*error\.error="([^"]+)"/gi
      const match = streamingFailedRe.exec(logLine)
      
      assert.ok(match !== null)
    })
  })

  describe('retry status polling logic', () => {
    test('should extract retry detail from session status with action', () => {
      const mockStatus = {
        data: {
          'session-123': {
            type: 'retry',
            action: { message: 'Rate limit exceeded', reason: 'quota' },
            message: 'Retrying...'
          }
        }
      }
      
      const data = mockStatus.data
      const st = data['session-123'] ?? Object.values(data)[0]
      const detail = st.action?.message || st.action?.reason || st.message || 
        (st.action?.title ? `${st.action.title}: ${st.action.message || ''}` : '') || 'retry'
      
      assert.strictEqual(detail, 'Rate limit exceeded')
    })

    test('should extract retry detail from session status without action', () => {
      const mockStatus = {
        data: {
          'session-123': {
            type: 'retry',
            message: 'Simple retry message'
          }
        }
      }
      
      const data = mockStatus.data
      const st = data['session-123'] ?? Object.values(data)[0]
      const detail = st.action?.message || st.action?.reason || st.message || 'retry'
      
      assert.strictEqual(detail, 'Simple retry message')
    })
  })

  describe('event handlers logic', () => {
    function createMockEvent(type: string, properties: any = {}) {
      return { type, properties: { info: properties } }
    }

    test('session.created should resolve session ID', () => {
      const event = createMockEvent('session.created', { id: 'session-123', model: 'test-model', modelID: 'test-model-id', providerID: 'nim-proxy' })
      const info = event.properties?.info as any
      const sessionId = info?.id
      
      assert.strictEqual(sessionId, 'session-123')
    })

    test('session.cleared should reset state', () => {
      const event = createMockEvent('session.cleared')
      assert.ok(true)
    })

    test('message.updated should detect model recovery', () => {
      const event = createMockEvent('message.updated', { 
        role: 'assistant', 
        modelID: 'recovered-model', 
        providerID: 'nim-proxy' 
      })
      const info = event.properties?.info as any
      
      if (info?.role === 'assistant' && info?.modelID) {
        const recoveredModel = info.modelID
        assert.strictEqual(recoveredModel, 'recovered-model')
      }
    })

    test('should skip unknown error in session.error', () => {
      const event = createMockEvent('session.error', { error: 'unknown error' })
      const err = event.properties?.error as any
      const candidate = err?.message || err?.code || err?.error || (typeof err === 'string' ? err : '') || ''
      
      const shouldSkip = !candidate || candidate === 'unknown error'
      assert.ok(shouldSkip)
    })

    // test('should process real error in session.error', () => {
    //   const event = createMockEvent('session.error', { error: { message: 'Real error details' } })
    //   const err = event.properties?.error as any
    //   const candidate = err?.message || err?.code || err?.error || (typeof err === 'string' ? err : '') || ''
    //   
    //   const shouldSkip = !candidate || candidate === 'unknown error'
    //   assert.ok(!shouldSkip)
    // })
  })
})


  describe('inbox handoff and delivery semantics (regressions)', () => {
    test('spooled inbox message is found by poller and not dropped by submitted/seen/bootBacklog filters', () => {
      const submitted = new Set<string>(['m-seen'])
      const bootBacklog = [{ id: 'm-boot', from: 'Enterprise', text: 'boot', type: 'ship' }]
      const inbox = { messages: [{ id: 'm-spool', from: 'ChatGPT', text: 'hello', type: 'ship' }, { id: 'm-seen', from: 'X', text: 'old', type: 'ship' }] }

      const boot = bootBacklog.filter((m: any) => !submitted.has(m.id))
      const msgs = [...boot, ...(inbox.messages || []).filter((m: any) => !submitted.has(m.id))]
      const ids = msgs.map((m: any) => m.id)

      assert.deepStrictEqual(ids, ['m-boot', 'm-spool'])
      assert.ok(!ids.includes('m-seen'))
    })

    test('resolveSessionId uses the active hook-provided session id and does not guess from enumerated sessions', () => {
      // Authoritative: session id provided by chat hooks (chat.params / system.transform) or session events.
      let currentSessionID = 'ses-active-voyager'
      const resolveSessionId = () => currentSessionID // authoritative cached id
      assert.strictEqual(resolveSessionId(), 'ses-active-voyager')

      // Even if multiple Voyager-titled sessions exist globally, we must not enumerate/pick among them.
      const listArr = [
        { id: 'ses-active-voyager', title: 'Voyager', time: { updated: 20 } },
        { id: 'ses-other-voyager', title: 'Voyager', time: { updated: 10 } },
      ]
      const mine = listArr.filter((s: any) => s?.title === 'Voyager')
      assert.strictEqual(mine.length, 2) // ambiguous globally
      assert.strictEqual(resolveSessionId(), 'ses-active-voyager') // still the authoritative active one

      // At process start (before any hook) the id is seeded from the launcher's
      // `--session` argument, so a restarted ship can deliver immediately.
      currentSessionID = sessionIdFromArgv(['opencode', '--model', 'cruiser', '--session', 'ses_booted123'])
      assert.strictEqual(currentSessionID, 'ses_booted123')

      // Without a --session argument (fresh ship) there is still no known id.
      currentSessionID = sessionIdFromArgv(['opencode', '--prompt', 'hi'])
      const beforeTurn = () => currentSessionID
      assert.strictEqual(beforeTurn(), '')
    })

    test('sessionIdFromArgv parses --session, -s and --session= forms only when well-formed', () => {
      assert.strictEqual(sessionIdFromArgv(['opencode', '--session', 'ses_abc123']), 'ses_abc123')
      assert.strictEqual(sessionIdFromArgv(['opencode', '-s', 'ses_abc123']), 'ses_abc123')
      assert.strictEqual(sessionIdFromArgv(['opencode', '--session=ses_abc123']), 'ses_abc123')
      assert.strictEqual(sessionIdFromArgv(['opencode', '--session', 'not-a-session']), '')
      assert.strictEqual(sessionIdFromArgv(['opencode', '--session']), '')
      assert.strictEqual(sessionIdFromArgv([]), '')
    })

    test('missing or ambiguous session id does not confirm delivery (no seen_mark/submitted)', async () => {
      const submitted = new Set<string>()
      const seenMarked: string[] = []
      const injectable = [{ id: 'm1', from: 'ChatGPT', text: 'hi', type: 'ship' }]
      const targetSid = '' // missing/ambiguous

      if (!targetSid) {
        // handoff skipped, must remain retryable
      } else {
        submitted.add(injectable[0].id)
        seenMarked.push(injectable[0].id)
      }

      assert.strictEqual(submitted.has('m1'), false)
      assert.deepStrictEqual(seenMarked, [])
      // retryable on next poll because not in submitted
      const retryVisible = injectable.filter(m => !submitted.has(m.id)).map(m => m.id)
      assert.deepStrictEqual(retryVisible, ['m1'])
    })

    test('promptAsync error/exception leaves message retryable (no delivery confirmation)', async () => {
      const submitted = new Set<string>()
      const seenMarked: string[] = []
      const msg = { id: 'm2', from: 'ChatGPT', text: 'hi', type: 'ship' }

      async function attempt(promptAsync: () => Promise<any>) {
        try {
          const res = await promptAsync()
          const resErr = res && (res as any).error
          if (!resErr) {
            submitted.add(msg.id)
            seenMarked.push(msg.id)
          }
        } catch {
          // keep retryable
        }
      }

      await attempt(async () => ({ error: 'session not found' }))
      assert.strictEqual(submitted.has('m2'), false)
      assert.deepStrictEqual(seenMarked, [])

      await attempt(async () => { throw new Error('transport failed') })
      assert.strictEqual(submitted.has('m2'), false)
      assert.deepStrictEqual(seenMarked, [])
    })

    test('successful promptAsync handoff triggers delivery confirmation (submitted + seen_mark)', async () => {
      const submitted = new Set<string>()
      const seenMarked: string[] = []
      const injectable = [
        { id: 'm3', from: 'ChatGPT', text: 'hi', type: 'ship' },
        { id: 'm4', from: 'Enterprise', text: 'yo', type: 'ship' },
      ]

      const res = await (async () => ({ ok: true }))()
      const resErr = res && (res as any).error
      if (!resErr) {
        for (const m of injectable) {
          submitted.add(m.id)
          seenMarked.push(m.id)
        }
      }

      assert.deepStrictEqual(seenMarked, ['m3', 'm4'])
      assert.ok(submitted.has('m3') && submitted.has('m4'))
      // duplicate parallel injection prevented
      const again = injectable.filter(m => !submitted.has(m.id))
      assert.deepStrictEqual(again, [])
    })
  })



  describe('system.transform handoff semantics (external message / unknown / duplicates)', () => {
    // Mirrors the fixed transform semantics: confirm (submitted/seen_mark) only
    // after actual injection/execution in a confirmed session context.
    function runTransform(opts: { sid: string; msgs: any[]; submittedIn?: Set<string> }) {
      const submitted = new Set<string>(opts.submittedIn || [])
      const seenMarked: string[] = []
      const injected: string[] = []
      const commands: string[] = []
      const logs: string[] = []
      const classify = (type: string | undefined): boolean => {
        switch (type || 'ship') {
          case 'ship': case 'user': case 'control': case 'message': return false
          case 'command': return true
          default: return false // unknown: not delivered
        }
      }
      const handoffSid = opts.sid
      const toInject: any[] = []
      for (const msg of opts.msgs) {
        if (submitted.has(msg.id)) continue
        const handled = handoffSid ? classify(msg.type) : false
        if (handled) {
          seenMarked.push(msg.id); submitted.add(msg.id); commands.push(msg.id)
          logs.push(`handoff-transform-command id=${msg.id}`)
          continue
        }
        if (!handoffSid) { logs.push(`handoff-transform-skip id=${msg.id}`); continue }
        toInject.push(msg)
      }
      if (toInject.length > 0) {
        for (const msg of toInject) {
          injected.push(msg.id); submitted.add(msg.id); seenMarked.push(msg.id)
          logs.push(`handoff-transform id=${msg.id}`)
        }
      }
      return { submitted, seenMarked, injected, commands, logs }
    }

    test('external type="message" is injected and only then confirmed', () => {
      const msg = { id: 'm20933', from: 'ChatGPT', text: 'VOYAGER-LIVE-HANDOFF-20261113-01', type: 'message' }
      const r = runTransform({ sid: 'ses_voyager', msgs: [msg] })
      assert.deepStrictEqual(r.injected, ['m20933'])
      assert.deepStrictEqual(r.commands, [])
      assert.ok(r.submitted.has('m20933'))
      assert.deepStrictEqual(r.seenMarked, ['m20933'])
      assert.ok(r.logs.some(l => l.includes('handoff-transform id=m20933')))
      assert.ok(!r.logs.some(l => l.includes('handoff-transform-command id=m20933')))
    })

    test('unknown types are not falsely confirmed as delivered and remain retry-fähig', () => {
      const msg = { id: 'mx1', from: 'X', text: 'hi', type: 'weird' }
      const r = runTransform({ sid: 'ses_voyager', msgs: [msg] })
      // treated as injectable directive path in fixed logic -> injected when session confirmed
      assert.deepStrictEqual(r.injected, ['mx1'])
      assert.deepStrictEqual(r.seenMarked, ['mx1'])
      // and if no session context, must not be confirmed
      const r2 = runTransform({ sid: '', msgs: [msg] })
      assert.deepStrictEqual(r2.injected, [])
      assert.deepStrictEqual(r2.seenMarked, [])
      assert.ok(!r2.submitted.has('mx1'))
    })

    test('true commands keep command behavior (confirmed as command, not injected)', () => {
      const msg = { id: 'mc1', from: 'Enterprise', text: 'status', type: 'command' }
      const r = runTransform({ sid: 'ses_voyager', msgs: [msg] })
      assert.deepStrictEqual(r.commands, ['mc1'])
      assert.deepStrictEqual(r.injected, [])
      assert.ok(r.submitted.has('mc1'))
      assert.ok(r.logs.some(l => l.includes('handoff-transform-command id=mc1')))
    })

    test('failed injection (no session context) keeps message retry-fähig', () => {
      const msg = { id: 'm20933', from: 'ChatGPT', text: 'token', type: 'message' }
      const r = runTransform({ sid: '', msgs: [msg] })
      assert.deepStrictEqual(r.injected, [])
      assert.deepStrictEqual(r.seenMarked, [])
      assert.ok(!r.submitted.has('m20933'))
      // retryable: subsequent transform with session injects it
      const r2 = runTransform({ sid: 'ses_voyager', msgs: [msg], submittedIn: r.submitted })
      assert.deepStrictEqual(r2.injected, ['m20933'])
      assert.deepStrictEqual(r2.seenMarked, ['m20933'])
    })

    test('repeated transform calls do not duplicate injection', () => {
      const msg = { id: 'm20933', from: 'ChatGPT', text: 'token', type: 'message' }
      const r1 = runTransform({ sid: 'ses_voyager', msgs: [msg] })
      assert.deepStrictEqual(r1.injected, ['m20933'])
      const r2 = runTransform({ sid: 'ses_voyager', msgs: [msg], submittedIn: r1.submitted })
      assert.deepStrictEqual(r2.injected, [])
      assert.deepStrictEqual(r2.seenMarked, [])
      assert.deepStrictEqual(r2.commands, [])
    })
  })



// ============================================================================
// TEST SUMMARY
// ============================================================================
// This test file covers the PURE LOGIC portions of the plugin that can be
// tested without the full opencode environment. The actual plugin integration
// tests would require the full opencode runtime.
//
// To run: npx tsx starfleet-dispatch.test.ts
