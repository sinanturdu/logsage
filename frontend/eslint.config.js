import js from '@eslint/js'
import tseslint from 'typescript-eslint'
import reactHooks from 'eslint-plugin-react-hooks'

export default tseslint.config(
  { ignores: ['dist', 'node_modules'] },
  js.configs.recommended,
  ...tseslint.configs.strict,
  {
    files: ['**/*.{ts,tsx}'],
    plugins: { 'react-hooks': reactHooks },
    rules: {
      ...reactHooks.configs.recommended.rules,
      // Untrusted data (log messages, attributes) is rendered as text only (ADR 0016).
      'no-restricted-syntax': [
        'error',
        {
          selector: "JSXAttribute[name.name='dangerouslySetInnerHTML']",
          message: 'Render untrusted data as text (use <LogText>); never as HTML.',
        },
        {
          selector: "MemberExpression[property.name='innerHTML'], MemberExpression[property.name='outerHTML']",
          message: 'Do not write HTML strings into the DOM.',
        },
      ],
      // No tokens or user data in browser storage: the session is an HttpOnly cookie.
      'no-restricted-globals': [
        'error',
        { name: 'localStorage', message: 'No browser storage for user data (ADR 0016).' },
        { name: 'sessionStorage', message: 'No browser storage for user data (ADR 0016).' },
      ],
      'no-restricted-properties': [
        'error',
        { object: 'window', property: 'localStorage', message: 'No browser storage for user data (ADR 0016).' },
        { object: 'window', property: 'sessionStorage', message: 'No browser storage for user data (ADR 0016).' },
      ],
    },
  },
  {
    // Tests read browser storage to assert that it stays empty.
    files: ['**/*.test.{ts,tsx}'],
    rules: { 'no-restricted-globals': 'off', 'no-restricted-properties': 'off' },
  },
)
