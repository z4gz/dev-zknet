/* eslint-disable */

// SWC compilation config for the spec files, inlined rather than read from
// `.spec.swcrc`: this package is `"type": "module"`, so Jest loads this config
// as ESM, where `__dirname` does not exist.
const swcJestConfig = {
  // Disable .swcrc look-up by SWC core because we're passing in swcJestConfig ourselves
  swcrc: false,
  jsc: {
    target: 'es2017',
    parser: {
      syntax: 'typescript',
      decorators: true,
      dynamicImport: true,
    },
    transform: {
      decoratorMetadata: true,
      legacyDecorator: true,
    },
    keepClassNames: true,
    externalHelpers: true,
    loose: true,
  },
  module: {
    type: 'es6',
  },
  sourceMaps: true,
  exclude: [],
};

export default {
  displayName: '@zknet/sdk',
  preset: '../../jest.preset.js',
  testEnvironment: 'node',
  transform: {
    '^.+\\.[tj]s$': ['@swc/jest', swcJestConfig],
  },
  moduleFileExtensions: ['ts', 'js', 'html'],
  coverageDirectory: 'test-output/jest/coverage',
};
