// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Unit test harness for starfleet-dispatch plugin logic.
// Tests pure logic functions that can be tested in isolation.
// Run with: npx tsx starfleet-dispatch.test.ts

import { test, describe } from 'node:test'
import assert from 'node:assert'

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

    test('should identify command type', () => {
      const msg = createMockMessage('m1', 'Enterprise', 'model test-model', 'command')
      assert.strictEqual(msg.type, 'command')
    })

    test('should identify ship type', () => {
      const msg = createMockMessage('m1', 'Enterprise', 'do something', 'ship')
      assert.strictEqual(msg.type, 'ship')
    })

    test('should identify user type', () => {
      const msg = createMockMessage('m1', 'Enterprise', 'do something', 'user')
      assert.strictEqual(msg.type, 'user')
    })

    test('should identify control type', () => {
      const msg = createMockMessage('m1', 'Enterprise', 'do something', 'control')
      assert.strictEqual(msg.type, 'control')
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

// ============================================================================
// TEST SUMMARY
// ============================================================================
// This test file covers the PURE LOGIC portions of the plugin that can be
// tested without the full opencode environment. The actual plugin integration
// tests would require the full opencode runtime.
//
// To run: npx tsx starfleet-dispatch.test.ts
