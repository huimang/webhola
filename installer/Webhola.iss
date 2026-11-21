#ifndef AppVersion
  #define AppVersion "0.2.0"
#endif
#define WiresharkVersion "4.6.9"
#define NpcapVersion "1.90"
#define JdkSHA256 "BBEBEA1469996B276683C9299C227B294F85C847039C82633218186A4AD4FBF2"
#define ArthasSHA256 "2F75EA954EAD5D02C4A743ECD783FD92F546A44EF3AEE437EA5066EB30AD07BF"
#define WiresharkSHA256 "BF9B5CE8A89F244C376A9B1A946276EAA06463DDE3E33069A34D7F102F5878CF"
#define NpcapSHA256 "682B57E1C05EF375931975C9D6DED40888F4BFDF9F4B11571E654F962F1199B6"
#ifndef LanguageFile
  #define LanguageFile "compiler:Languages\ChineseSimplified.isl"
#endif

[Setup]
AppId={{82AC2B57-C2BF-44C6-9931-135832C8BED7}
AppName=Webhola
AppVersion={#AppVersion}
AppPublisher=Webhola
DefaultDirName={localappdata}\Programs\Webhola
DefaultGroupName=Webhola
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
OutputDir=..\native\build
OutputBaseFilename=Webhola-Setup-{#AppVersion}-x64
SetupIconFile=..\assets\brand\webhola-icon.ico
UninstallDisplayIcon={app}\Webhola.exe
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
DisableProgramGroupPage=yes
UsePreviousTasks=no
CloseApplications=yes
RestartApplications=no
VersionInfoVersion={#AppVersion}.0

[Languages]
Name: "chinesesimp"; MessagesFile: "{#LanguageFile}"
Name: "english"; MessagesFile: "compiler:Default.isl"

#include "locales\messages.isl"

[Tasks]
Name: "capture"; Description: "{cm:TaskCapture}"
Name: "jvm"; Description: "{cm:TaskJvm}"
Name: "desktopicon"; Description: "{cm:TaskDesktopIcon}"; Flags: unchecked

[Files]
Source: "..\native\build\Webhola.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "Dependencies.ps1"; Flags: dontcopy
Source: "JavaDependencies.ps1"; Flags: dontcopy
Source: "I18n.ps1"; Flags: dontcopy
Source: "locales\*.json"; Flags: dontcopy

[Icons]
Name: "{userprograms}\Webhola"; Filename: "{app}\Webhola.exe"
Name: "{userdesktop}\Webhola"; Filename: "{app}\Webhola.exe"; Tasks: desktopicon

[Run]
Filename: "{app}\Webhola.exe"; Description: "{cm:RunWebhola}"; Flags: nowait postinstall skipifsilent

[Code]
var
  Downloads: TDownloadWizardPage;
  NeedWireshark, NeedNpcap, DownloadsReady, CaptureReady, RestartNeeded: Boolean;
  NeedJdk, NeedArthas, JavaReady: Boolean;
  JavaMessage: String;
  DependencyMessage: String;

function Quote(S: String): String;
begin
  Result := '"' + S + '"';
end;

function DependencyLanguage: String;
begin
  if ActiveLanguage = 'english' then Result := 'en' else Result := 'zh-CN';
end;

function RunJavaSupport(Action: String): Boolean;
var
  Code: Integer;
  Args: String;
begin
  Args := '-NoProfile -NonInteractive -ExecutionPolicy Bypass -File ' +
    Quote(ExpandConstant('{tmp}\JavaDependencies.ps1')) + ' -Action ' + Action +
    ' -StateFile ' + Quote(ExpandConstant('{tmp}\webhola-java.ini')) +
    ' -DependencyRoot ' + Quote(ExpandConstant('{app}\webhola-dependencies')) +
    ' -JdkPackage ' + Quote(ExpandConstant('{tmp}\temurin21-jdk.zip')) +
    ' -ArthasPackage ' + Quote(ExpandConstant('{tmp}\arthas-4.3.5-bin.zip'));
  Result := Exec(ExpandConstant('{sys}\WindowsPowerShell\v1.0\powershell.exe'),
    Args + ' -Language ' + DependencyLanguage, '', SW_HIDE, ewWaitUntilTerminated, Code);
  if Result then Result := Code = 0;
end;

function JavaState(Name: String): String;
begin
  Result := GetIniString('State', Name, '', ExpandConstant('{tmp}\webhola-java.ini'));
end;

function RunSupport(Action, PackagePath: String): Boolean;
var
  Code: Integer;
  Args: String;
begin
  Args := '-NoProfile -NonInteractive -ExecutionPolicy Bypass -File ' +
    Quote(ExpandConstant('{tmp}\Dependencies.ps1')) + ' -Action ' + Action +
    ' -StateFile ' + Quote(ExpandConstant('{tmp}\webhola-dependencies.ini'));
  if PackagePath <> '' then Args := Args + ' -PackagePath ' + Quote(PackagePath);
  Result := Exec(ExpandConstant('{sys}\WindowsPowerShell\v1.0\powershell.exe'),
    Args + ' -Language ' + DependencyLanguage, '', SW_HIDE, ewWaitUntilTerminated, Code);
  if Result then Result := Code = 0;
end;

function StateValue(Name: String): String;
begin
  Result := GetIniString('State', Name, '', ExpandConstant('{tmp}\webhola-dependencies.ini'));
end;

function DownloadProgress(const Url, FileName: String; const Progress, ProgressMax: Int64): Boolean;
begin
  Result := True;
end;

procedure InitializeWizard;
begin
  ExtractTemporaryFile('I18n.ps1');
  ExtractTemporaryFile('zh-CN.json');
  ExtractTemporaryFile('en.json');
  ExtractTemporaryFile('Dependencies.ps1');
  ExtractTemporaryFile('JavaDependencies.ps1');
  Downloads := CreateDownloadPage(CustomMessage('PrepareDependencies'),
    CustomMessage('DownloadVerification'), @DownloadProgress);
end;

function NextButtonClick(CurPageID: Integer): Boolean;
begin
  Result := True;
  if CurPageID <> wpReady then Exit;
  DownloadsReady := False;
  NeedWireshark := False;
  NeedNpcap := False;
  NeedJdk := False;
  NeedArthas := False;
  if WizardIsTaskSelected('capture') then begin
  WizardForm.StatusLabel.Caption := CustomMessage('DetectCapture');
  if not RunSupport('Detect', '') then begin
    MsgBox(CustomMessage('DetectCaptureFailed'), mbError, MB_OK);
    Result := False;
    Exit;
  end;
  NeedWireshark := StateValue('TSharkPath') = '';
  NeedNpcap := StateValue('NpcapInstalled') <> '1';
  end;
  if WizardIsTaskSelected('jvm') then begin
    WizardForm.StatusLabel.Caption := CustomMessage('DetectJava');
    if not RunJavaSupport('Detect') then begin
      MsgBox(CustomMessage('DetectJavaFailed') + JavaState('Error'), mbError, MB_OK);
      Result := False;
      Exit;
    end;
    NeedJdk := JavaState('JavaPath') = '';
    NeedArthas := JavaState('BootPath') = '';
  end;
  if not NeedWireshark and not NeedNpcap and not NeedJdk and not NeedArthas then begin
    DownloadsReady := True;
    Exit;
  end;
  Downloads.Clear;
  if NeedWireshark then Downloads.Add(
    'https://www.wireshark.org/download/win64/all-versions/Wireshark-{#WiresharkVersion}-x64.exe',
    'Wireshark-{#WiresharkVersion}-x64.exe', '{#WiresharkSHA256}');
  if NeedNpcap then Downloads.Add('https://npcap.com/dist/npcap-{#NpcapVersion}.exe',
    'npcap-{#NpcapVersion}.exe', '{#NpcapSHA256}');
  if NeedJdk then Downloads.Add(
    'https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.13.1%2B2/OpenJDK21U-jdk_x64_windows_hotspot_21.0.13.1_2.zip',
    'temurin21-jdk.zip', '{#JdkSHA256}');
  if NeedArthas then Downloads.Add(
    'https://repo.maven.apache.org/maven2/com/taobao/arthas/arthas-packaging/4.3.5/arthas-packaging-4.3.5-bin.zip',
    'arthas-4.3.5-bin.zip', '{#ArthasSHA256}');
  Downloads.Show;
  try
    try
      Downloads.Download;
      if NeedWireshark then
        if not RunSupport('VerifyWireshark', ExpandConstant('{tmp}\Wireshark-{#WiresharkVersion}-x64.exe')) then
          RaiseException(CustomMessage('WiresharkSignatureFailed'));
      if NeedNpcap then
        if not RunSupport('VerifyNpcap', ExpandConstant('{tmp}\npcap-{#NpcapVersion}.exe')) then
          RaiseException(CustomMessage('NpcapSignatureFailed'));
      DownloadsReady := True;
    except
      if not Downloads.AbortedByUser then
        MsgBox(GetExceptionMessage + #13#10 + CustomMessage('RetryNetwork'), mbError, MB_OK);
      Result := False;
    end;
  finally
    Downloads.Hide;
  end;
end;

function InstallDependency(FileName, Arguments, VerifyAction: String; Visible: Boolean): Boolean;
var
  Code, ShowMode: Integer;
begin
  Result := False;
  if not RunSupport(VerifyAction, FileName) then begin
    DependencyMessage := CustomMessage('DependencySignatureFailed');
    Exit;
  end;
  ShowMode := SW_HIDE;
  if Visible then ShowMode := SW_SHOWNORMAL;
  if not ShellExec('runas', FileName, Arguments, '', ShowMode, ewWaitUntilTerminated, Code) then begin
    DependencyMessage := CustomMessage('DependencyCancelled');
    Exit;
  end;
  if (Code = 3010) or (Code = 1641) then RestartNeeded := True;
  Result := (Code = 0) or (Code = 3010) or (Code = 1641);
  if not Result then DependencyMessage := CustomMessage('DependencyExitCode') + IntToStr(Code);
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
  Result := '';
  if (WizardIsTaskSelected('capture') or WizardIsTaskSelected('jvm')) and not DownloadsReady then
    Result := CustomMessage('DependencyInteractiveRequired');
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  ContinueSetup: Boolean;
begin
  if CurStep <> ssPostInstall then Exit;
  if not DownloadsReady then begin
    DependencyMessage := CustomMessage('DependencyDownloadRequired');
    Exit;
  end;
  ContinueSetup := True;
  if WizardIsTaskSelected('capture') then begin
  if NeedWireshark then begin
    WizardForm.StatusLabel.Caption := CustomMessage('InstallCapture');
    ContinueSetup := InstallDependency(ExpandConstant('{tmp}\Wireshark-{#WiresharkVersion}-x64.exe'),
      '/S /desktopicon=no /quicklaunchicon=no', 'VerifyWireshark', False);
  end;
  if ContinueSetup and NeedNpcap then begin
    WizardForm.StatusLabel.Caption := CustomMessage('NpcapLicense');
    ContinueSetup := InstallDependency(ExpandConstant('{tmp}\npcap-{#NpcapVersion}.exe'),
      '', 'VerifyNpcap', True);
  end;
  if ContinueSetup then begin
    WizardForm.StatusLabel.Caption := CustomMessage('VerifyCapture');
    if RunSupport('Configure', '') then CaptureReady := StateValue('CaptureReady') = '1';
    if not CaptureReady then DependencyMessage := StateValue('Error');
  end;
  end;
  if WizardIsTaskSelected('jvm') then begin
    WizardForm.StatusLabel.Caption := CustomMessage('PrepareJava');
    if RunJavaSupport('Install') then JavaReady := JavaState('JavaReady') = '1';
    if not JavaReady then JavaMessage := JavaState('Error');
  end;
end;

procedure CurPageChanged(CurPageID: Integer);
begin
  if CurPageID = wpSelectTasks then
    WizardForm.SelectTasksLabel.Caption := CustomMessage('SelectComponents') + #13#10 +
      CustomMessage('NpcapInteractive');
  if CurPageID = wpFinished then begin
    WizardForm.FinishedLabel.Caption := CustomMessage('InstallComplete');
    if WizardIsTaskSelected('capture') then begin
      if CaptureReady then WizardForm.FinishedLabel.Caption := WizardForm.FinishedLabel.Caption + #13#10 + CustomMessage('CaptureReady')
      else WizardForm.FinishedLabel.Caption := WizardForm.FinishedLabel.Caption + #13#10 + CustomMessage('CaptureNotReady') + DependencyMessage;
    end;
    if WizardIsTaskSelected('jvm') then begin
      if JavaReady then WizardForm.FinishedLabel.Caption := WizardForm.FinishedLabel.Caption + #13#10 + CustomMessage('JavaReady')
      else WizardForm.FinishedLabel.Caption := WizardForm.FinishedLabel.Caption + #13#10 + CustomMessage('JavaNotReady') + JavaMessage;
    end;
  end;
end;

function NeedRestart: Boolean;
begin
  Result := RestartNeeded;
end;
