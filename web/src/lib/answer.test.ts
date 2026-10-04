import { describe, expect, it } from 'vitest'
import { collectCitationIds, parseAnswer, tokenizeAnswer } from './answer'

describe('tokenizeAnswer', () => {
  it('turns footnote markers into citation tokens', () => {
    const tokens = tokenizeAnswer('这家很安静 [^3]，评分 4.5')
    expect(tokens).toEqual([
      { kind: 'text', text: '这家很安静 ' },
      { kind: 'citation', evidenceId: 3, label: '3' },
      { kind: 'text', text: '，评分 4.5' },
    ])
  })

  it('keeps several markers in one line', () => {
    const tokens = tokenizeAnswer('见 [^1] 与 [^2]')
    expect(tokens.filter((t) => t.kind === 'citation')).toHaveLength(2)
  })

  it('returns plain text untouched when there are no markers', () => {
    expect(tokenizeAnswer('没有引用')).toEqual([{ kind: 'text', text: '没有引用' }])
  })
})

describe('collectCitationIds', () => {
  it('returns first-seen order without duplicates', () => {
    expect(collectCitationIds('a [^2] b [^1] c [^2]')).toEqual([2, 1])
  })

  it('returns nothing for an answer with no citations', () => {
    expect(collectCitationIds('没有引用')).toEqual([])
  })
})

describe('parseAnswer', () => {
  it('splits paragraphs on blank lines', () => {
    const parsed = parseAnswer('第一段\n\n第二段')
    expect(parsed.segments).toHaveLength(2)
    expect(parsed.segments[0].lines).toEqual(['第一段'])
  })

  it('groups consecutive bullets into one list', () => {
    const parsed = parseAnswer('- 安静\n- 适合约会')
    expect(parsed.segments).toHaveLength(1)
    expect(parsed.segments[0].kind).toBe('list')
    expect(parsed.segments[0].lines).toEqual(['安静', '适合约会'])
  })

  it('lifts FOLLOWUPS out of the answer body', () => {
    const parsed = parseAnswer('结论一段\n\nFOLLOWUPS:\n1. 第二家安静吗？\n2. 有预约吗？')
    expect(parsed.segments).toHaveLength(1)
    expect(parsed.followups).toEqual(['第二家安静吗？', '有预约吗？'])
  })

  it('accepts an inline followup list', () => {
    const parsed = parseAnswer('FOLLOWUPS: 第二家安静吗？ | 有预约吗？')
    expect(parsed.followups).toEqual(['第二家安静吗？', '有预约吗？'])
  })

  it('yields nothing for empty content', () => {
    expect(parseAnswer('')).toEqual({ segments: [], followups: [] })
  })
})
