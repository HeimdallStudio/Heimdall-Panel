import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

import { describe, expect, it } from 'vitest';

function source(relativePath: string): string {
  return readFileSync(resolve(process.cwd(), relativePath), 'utf8');
}

function between(
  value: string,
  startMarker: string,
  endMarker: string,
): string {
  const start = value.indexOf(startMarker);
  const end = value.indexOf(endMarker, start + startMarker.length);

  expect(start, `missing start marker: ${startMarker}`).toBeGreaterThanOrEqual(0);
  expect(end, `missing end marker: ${endMarker}`).toBeGreaterThan(start);

  return value.slice(start, end);
}

const transportSummary =
  "<summary>{t('pages.inbounds.form.profileTransportSettings')}</summary>";
const securitySummary =
  "<summary>{t('pages.inbounds.form.profileSecuritySettings')}</summary>";
const advancedSummary =
  "<summary>{t('pages.inbounds.form.profileAdvancedSettings')}</summary>";

describe('subscription profile field ownership', () => {
  it('keeps transport-owned controls inside Transport Settings', () => {
    const editor = source(
      'src/pages/inbounds/form/transport/subscription-profile-editor.tsx',
    );
    const transportSection = between(
      editor,
      transportSummary,
      securitySummary,
    );

    expect(transportSection).toContain("name={[fieldName, 'network']}");
    expect(transportSection).toContain('TransportSettingsFields');
    expect(transportSection).toContain('FinalMaskForm');
    expect(transportSection).toContain('ClientSockoptForm');
    expect(transportSection).toContain('ProfileRuntimeSockoptFields');
    expect(transportSection).not.toContain('runtimeEnabled');

    expect(transportSection).not.toContain(
      'ProfileRuntimeServerSecurityFields',
    );
  });

  it('keeps client and server security controls inside Security', () => {
    const editor = source(
      'src/pages/inbounds/form/transport/subscription-profile-editor.tsx',
    );
    const securitySection = between(
      editor,
      securitySummary,
      advancedSummary,
    );

    expect(securitySection).toContain("name={[fieldName, 'security']}");
    expect(securitySection).toContain('ProfileClientSecurityFields');
    expect(securitySection).toContain(
      'ProfileRuntimeServerSecurityFields',
    );
    expect(securitySection).toContain("effectiveSecurity !== 'none'");
    expect(securitySection).not.toContain('runtimeEnabled');

    expect(securitySection).not.toContain('ProfileRuntimeSockoptFields');
  });

  it('removes every manual Runtime Listener control from the editor', () => {
    const editor = source(
      'src/pages/inbounds/form/transport/subscription-profile-editor.tsx',
    );
    const styles = source(
      'src/pages/inbounds/form/transport/external-proxy.css',
    );

    for (const hiddenControl of [
      'profileRuntimeSettings',
      'profileRuntimeListener',
      'profileRuntimeId',
      'profileRuntimeMode',
      'profileRuntimeListen',
      'profileRuntimePort',
      'profileRuntimePreview',
      'profileRuntimeConflictTitle',
      'planSubscriptionProfileRuntimeTopology',
      'formatSubscriptionRuntimeSocket',
      'toggleRuntime',
      'runtimeEnabled',
      '>RUNTIME<',
    ]) {
      expect(editor).not.toContain(hiddenControl);
    }

    for (const topologyField of ['enabled', 'mode', 'listen', 'port']) {
      expect(editor).not.toContain(
        `name={[fieldName, 'runtime', '${topologyField}']}`,
      );
    }

    expect(styles).not.toContain('.ext-proxy-runtime-preview');
  });

  it('keeps hidden runtime metadata normalized before Save', () => {
    const profile = source('src/lib/xray/subscription-profile.ts');
    const modal = source('src/pages/inbounds/form/InboundFormModal.tsx');
    const planner = source(
      'src/lib/xray/subscription-profile-runtime-bindings.ts',
    );

    expect(profile).toContain('AUTOMATIC_RUNTIME_TOPOLOGY_FIELDS');
    expect(profile).not.toContain('createUniqueRuntimeProfileId');
    expect(profile).toContain('delete runtime.id');
    expect(profile).toContain('normalizedRuntimeMetadata');
    expect(profile).toContain('isModernSubscriptionProfile');
    expect(profile).toContain('hasAutomaticRuntimeMarker');
    expect(modal).toContain('normalizeSubscriptionProfilesForProtocolSave');
    expect(modal).toContain('findSubscriptionProfileRuntimeConflicts');
    expect(planner).toContain('!isModernSubscriptionProfile(profile)');
    expect(planner).toContain('!hasAutomaticRuntimeMarker(profile)');
    expect(planner).not.toContain('profile.runtime?.enabled');
    expect(planner).not.toContain('profile.runtime.port');
    expect(planner).not.toContain('profile.runtime.listen');
  });

  it('does not duplicate selectors or moved controls in Advanced', () => {
    const editor = source(
      'src/pages/inbounds/form/transport/subscription-profile-editor.tsx',
    );
    const advancedSection = between(
      editor,
      advancedSummary,
      '\nfunction TransportSettingsFields(',
    );

    expect(editor.match(/name=\{\[fieldName, 'network'\]\}/g)).toHaveLength(1);
    expect(editor.match(/name=\{\[fieldName, 'security'\]\}/g)).toHaveLength(1);
    expect(advancedSection).toContain('profileMuxMode');
    expect(advancedSection).not.toContain('FinalMaskForm');
    expect(advancedSection).not.toContain('ClientSockoptForm');
    expect(advancedSection).not.toContain('ProfileRuntimeSockoptFields');
    expect(advancedSection).not.toContain(
      'ProfileRuntimeServerSecurityFields',
    );
  });

  it('does not expose manual flow override or common-field helper copy', () => {
    const editor = source(
      'src/pages/inbounds/form/transport/subscription-profile-editor.tsx',
    );

    expect(editor).not.toContain("name={[fieldName, 'flow']}");
    expect(editor).not.toContain('pages.inbounds.form.profileFlow');
    expect(editor).not.toContain('pages.inbounds.form.profileFlowHint');
    expect(editor).not.toContain(
      'pages.inbounds.form.blankUsesInboundAddress',
    );
  });

  it('links the default profile port while keeping later profiles on 1995', () => {
    const modal = source('src/pages/inbounds/form/InboundFormModal.tsx');
    const externalProxy = source(
      'src/pages/inbounds/form/transport/external-proxy.tsx',
    );
    const editor = source(
      'src/pages/inbounds/form/transport/subscription-profile-editor.tsx',
    );

    expect(modal).toContain('normalizeSubscriptionProfilesForProtocolSave');
    expect(modal).toContain('findSubscriptionProfileRuntimeConflicts');
    expect(editor).not.toContain('planSubscriptionProfileRuntimeTopology');
    expect(modal).toContain('externalProxy: [createSubscriptionProfileDraft(port)]');
    expect(externalProxy).toContain(
      'createSubscriptionProfileDraft(parentPort ?? undefined)',
    );
    expect(externalProxy.match(/createSubscriptionProfileDraft\(\)/g)).toHaveLength(1);
    expect(externalProxy).toContain('isDefaultProfile={index === 0}');
    expect(externalProxy).toContain("useWatch({ control, name: 'port' })");
    expect(externalProxy).not.toContain("Form.useWatch('port', form)");
    expect(externalProxy).toContain(
      "['streamSettings', 'externalProxy', 0, 'port']",
    );
    expect(externalProxy).toContain("'streamSettings.externalProxy.0.port'");
    expect(externalProxy).toContain('if (index > 0) remove(field.name)');
    expect(externalProxy).toContain('if (index > 1) move(field.name, field.name - 1)');
    expect(editor).toContain('disabled={displayIndex <= 2}');
    expect(editor).toContain(
      'disabled={isDefaultProfile || displayIndex === totalProfiles}',
    );
    expect(editor).toContain('disabled={isDefaultProfile}');
  });

  it('renders the client ECH config path exactly once', () => {
    const securityFields = source(
      'src/pages/inbounds/form/transport/subscription-profile-security-fields.tsx',
    );
    const echPath = /'tlsSettings',\s*'settings',\s*'echConfigList'/g;

    expect(securityFields.match(echPath)).toHaveLength(1);
    expect(securityFields).toContain("'runtime', 'tlsSettings', 'echServerKeys'");
    expect(securityFields).toContain('actions.generateEch');
  });
});
