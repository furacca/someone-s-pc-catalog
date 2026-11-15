import { afterEach, expect, test } from 'bun:test'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { getLastEdit, smartGlob } from './compiler/utils/util'

const sourceDateEpoch = process.env.SOURCE_DATE_EPOCH
afterEach(() => {
  if (sourceDateEpoch === undefined) delete process.env.SOURCE_DATE_EPOCH
  else process.env.SOURCE_DATE_EPOCH = sourceDateEpoch
})

test('unknown source dates use the pinned commit timestamp', () => {
  process.env.SOURCE_DATE_EPOCH = '1700000000'
  expect(getLastEdit('not-in-the-git-cache.ts')).toBe('2023-11-14T22:13:20.000Z')
})

test('unknown source dates never fall back to the current clock', () => {
  for (const value of [undefined, '', 'not-a-date', '-1', '1e3']) {
    if (value === undefined) delete process.env.SOURCE_DATE_EPOCH
    else process.env.SOURCE_DATE_EPOCH = value
    expect(() => getLastEdit('not-in-the-git-cache.ts')).toThrow('SOURCE_DATE_EPOCH')
  }
})

test('source enumeration has a fixed order before derived summaries are computed', async () => {
  const root = mkdtempSync(join(tmpdir(), 'spc-compiler-order-'))
  try {
    for (const name of ['z.ts', 'a.ts', 'm.ts']) writeFileSync(join(root, name), '')
    const expected = ['a.ts', 'm.ts', 'z.ts'].map((name) => join(root, name))
    expect(await smartGlob(join(root, '*.ts'))).toEqual(expected)
    expect(await smartGlob(join(root, '*.ts'))).toEqual(expected)
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})
