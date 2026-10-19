// The CodeMirror 6 bundle of the editor tab (web/editor.js), built into
// web/vendor/codemirror.js by scripts/build-codemirror.sh. It sets
// window.IwatoCodeMirror: the pieces the editor composes, a basic setup
// (no autocompletion, no linting), the highlighting with the page's
// colours, and the languages by the names editor.js's languageOf gives.
import { EditorView, keymap, lineNumbers, highlightActiveLineGutter, highlightSpecialChars, drawSelection, dropCursor, rectangularSelection, crosshairCursor, highlightActiveLine } from '@codemirror/view';
import { EditorState, EditorSelection } from '@codemirror/state';
import { defaultKeymap, history, historyKeymap, indentMore, indentLess } from '@codemirror/commands';
import { searchKeymap, highlightSelectionMatches } from '@codemirror/search';
import { indentOnInput, indentUnit, syntaxHighlighting, HighlightStyle, bracketMatching, foldGutter, foldKeymap, StreamLanguage } from '@codemirror/language';
import { tags } from '@lezer/highlight';
import { javascript } from '@codemirror/lang-javascript';
import { json } from '@codemirror/lang-json';
import { html } from '@codemirror/lang-html';
import { css } from '@codemirror/lang-css';
import { markdown } from '@codemirror/lang-markdown';
import { python } from '@codemirror/lang-python';
import { go } from '@codemirror/lang-go';
import { rust } from '@codemirror/lang-rust';
import { yaml } from '@codemirror/lang-yaml';
import { sql } from '@codemirror/lang-sql';
import { shell } from '@codemirror/legacy-modes/mode/shell';
import { dockerFile } from '@codemirror/legacy-modes/mode/dockerfile';
import { ruby } from '@codemirror/legacy-modes/mode/ruby';
import { toml } from '@codemirror/legacy-modes/mode/toml';

// Colours are CSS variables of web/styles.css (--code-*).
const v = (name) => 'var(--code-' + name + ')';
const highlight = HighlightStyle.define([
  { tag: [tags.keyword, tags.operatorKeyword, tags.modifier, tags.controlKeyword, tags.definitionKeyword, tags.moduleKeyword], color: v('keyword') },
  { tag: [tags.string, tags.special(tags.string), tags.character, tags.regexp, tags.inserted], color: v('string') },
  { tag: [tags.number, tags.bool, tags.null, tags.atom, tags.unit], color: v('number') },
  { tag: [tags.comment, tags.lineComment, tags.blockComment, tags.docComment, tags.meta, tags.processingInstruction], color: v('comment'), fontStyle: 'italic' },
  { tag: [tags.function(tags.variableName), tags.function(tags.propertyName), tags.macroName, tags.labelName], color: v('function') },
  { tag: [tags.typeName, tags.className, tags.namespace, tags.standard(tags.typeName), tags.tagName, tags.angleBracket], color: v('type') },
  { tag: [tags.propertyName, tags.attributeName, tags.definition(tags.propertyName)], color: v('property') },
  { tag: [tags.definition(tags.variableName), tags.special(tags.variableName), tags.self, tags.local(tags.variableName)], color: v('variable') },
  { tag: [tags.operator, tags.punctuation, tags.separator, tags.bracket, tags.derefOperator], color: v('punctuation') },
  { tag: [tags.heading, tags.heading1, tags.heading2, tags.heading3], color: v('heading'), fontWeight: 'bold' },
  { tag: [tags.link, tags.url], color: v('link'), textDecoration: 'underline' },
  { tag: tags.emphasis, fontStyle: 'italic' },
  { tag: tags.strong, fontWeight: 'bold' },
  { tag: tags.strikethrough, textDecoration: 'line-through' },
  { tag: [tags.deleted, tags.invalid], color: v('invalid') },
  { tag: [tags.escape, tags.special(tags.regexp)], color: v('escape') }
]);

export const basicSetup = [
  lineNumbers(),
  highlightActiveLineGutter(),
  highlightSpecialChars(),
  history(),
  foldGutter(),
  drawSelection(),
  dropCursor(),
  EditorState.allowMultipleSelections.of(true),
  indentOnInput(),
  syntaxHighlighting(highlight),
  bracketMatching(),
  rectangularSelection(),
  crosshairCursor(),
  highlightActiveLine(),
  highlightSelectionMatches(),
  keymap.of([...defaultKeymap, ...searchKeymap, ...historyKeymap, ...foldKeymap])
];

const languages = {
  javascript: () => javascript(),
  jsx: () => javascript({ jsx: true }),
  typescript: () => javascript({ typescript: true }),
  tsx: () => javascript({ jsx: true, typescript: true }),
  json: () => json(),
  html: () => html(),
  css: () => css(),
  markdown: () => markdown(),
  python: () => python(),
  go: () => go(),
  rust: () => rust(),
  yaml: () => yaml(),
  sql: () => sql(),
  shell: () => StreamLanguage.define(shell),
  dockerfile: () => StreamLanguage.define(dockerFile),
  ruby: () => StreamLanguage.define(ruby),
  toml: () => StreamLanguage.define(toml)
};

// language gives the highlighting for a name of languageOf ([] for plain
// text or a name it does not know), made once per name.
const made = new Map();
export function language(name) {
  if (!Object.prototype.hasOwnProperty.call(languages, name)) return [];
  if (!made.has(name)) made.set(name, languages[name]());
  return made.get(name);
}
export const languageNames = Object.keys(languages);

export { EditorView, EditorState, EditorSelection, keymap, indentUnit, indentMore, indentLess };
