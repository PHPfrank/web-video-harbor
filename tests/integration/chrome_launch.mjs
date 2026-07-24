export function selectChromeLaunch({
  chromePath,
  chromeArguments,
  platform = process.platform,
  runtimeArch = process.arch,
  nativeArm64Available = false,
}) {
  if (platform === 'darwin' && runtimeArch === 'x64' && nativeArm64Available) {
    return {
      command: '/usr/bin/arch',
      arguments: ['-arm64', chromePath, ...chromeArguments],
    };
  }
  return { command: chromePath, arguments: chromeArguments };
}
