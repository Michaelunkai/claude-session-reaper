param([switch]$DryRun,[switch]$NoChats,[int]$KeepChats=0,[int]$Max=600)
# Live driver for the OPEN Claude desktop app (no restart). Code mode: unpin + Archive every session that is not Running.
# Chat mode: Delete every chat (chats have no archive). Uses UI Automation + real mouse clicks.
$ErrorActionPreference='Stop'
Add-Type -AssemblyName UIAutomationClient,UIAutomationTypes
Add-Type @"
using System;using System.Runtime.InteropServices;
public class UiW{[DllImport("user32.dll")]public static extern bool SetProcessDPIAware();[DllImport("user32.dll")]public static extern bool SetCursorPos(int x,int y);[DllImport("user32.dll")]public static extern void mouse_event(uint f,int x,int y,uint d,int e);[DllImport("user32.dll")]public static extern bool GetCursorPos(out POINT p);public struct POINT{public int X;public int Y;}}
"@
[UiW]::SetProcessDPIAware()|Out-Null
$A=[System.Windows.Automation.AutomationElement]
$ALLC=[System.Windows.Automation.Condition]::TrueCondition
function Win{ $A::RootElement.FindAll('Children',$ALLC) | ? { $_.Current.Name -eq 'Claude' -and $_.Current.ClassName -eq 'Chrome_WidgetWin_1' } | Select -First 1 }
function Clk($e){ $r=$e.Current.BoundingRectangle; [UiW]::SetCursorPos([int]($r.X+$r.Width/2),[int]($r.Y+$r.Height/2))|Out-Null; Start-Sleep -Milliseconds 220; [UiW]::mouse_event(2,0,0,0,0);Start-Sleep -Milliseconds 70;[UiW]::mouse_event(4,0,0,0,0) }
function Kids($w){ $w.FindAll('Descendants',$ALLC) }
$nav='^(New|Search|Projects|Create project|Artifacts|New from a template|Scheduled|New task|Plugins|More navigation items|Pinned|Recents|Group and sort|Filter and group recents|View all|Code settings|Hide sidebar|Show sidebar|Back|Forward|Menu|Resize sidebar|Michael.*)$'
$script:fail=@{}
function Rows($w){
  $all=Kids $w; $more=@(); $btn=@()
  foreach($e in $all){ if($e.Current.ControlType -ne [System.Windows.Automation.ControlType]::Button){continue}
    $r=$e.Current.BoundingRectangle; if($r.X -gt 640 -or $r.Y -lt 60 -or $r.Y -gt 2100 -or $r.Width -le 0){continue}
    if($e.Current.Name -like 'More options*'){ if($r.X -ge 555 -and $r.X -le 600){$more+=$e} } elseif($e.Current.Name -notmatch $nav -and $r.Width -gt 300){ $btn+=$e } }
  $rows=@(); foreach($b in $btn){ $by=$b.Current.BoundingRectangle.Y; $m=$more | ? { [math]::Abs($_.Current.BoundingRectangle.Y-($by+6)) -lt 8 } | Select -First 1; if($m){ $rows+=[pscustomobject]@{Name=$b.Current.Name;Btn=$b;More=$m} } }
  $rows
}
function MenuItem($w,$re){ for($i=0;$i -lt 8;$i++){ Start-Sleep -Milliseconds 400; foreach($e in (Kids (Win))){ if($e.Current.ControlType -eq [System.Windows.Automation.ControlType]::MenuItem -and $e.Current.Name -match $re){ return $e } } } $null }
# Click the row's "More options" button, found fresh by name (rows reorder live), and confirm the opened menu is for THAT row.
function OpenMenu($moreName){
  for($t=0;$t -lt 3;$t++){
    $w=Win; $b=$w.FindFirst('Descendants',(New-Object System.Windows.Automation.PropertyCondition($A::NameProperty,$moreName)))
    if(-not $b){ return $false }
    Clk $b; Start-Sleep -Milliseconds 900
    foreach($e in (Kids (Win))){ if($e.Current.ControlType -eq [System.Windows.Automation.ControlType]::Menu -and $e.Current.Name -eq $moreName){ return $true } }
    Esc; Start-Sleep -Milliseconds 400
  }
  $false
}
function Esc{ Add-Type -AssemblyName System.Windows.Forms; [System.Windows.Forms.SendKeys]::SendWait('{ESC}') }
function Mode($w,$name){
  for($t=0;$t -lt 5;$t++){
    $w=Win; if((CurMode $w) -eq $name){ return $true }
    $rb=(Kids $w) | ? { $_.Current.ControlType -eq [System.Windows.Automation.ControlType]::RadioButton -and $_.Current.Name -eq $name } | Select -First 1
    if($rb){ Clk $rb }; Start-Sleep -Seconds 2
  }
  $w=Win; (CurMode $w) -eq $name
}
function CurMode($w){ foreach($e in (Kids $w)){ if($e.Current.ControlType -eq [System.Windows.Automation.ControlType]::RadioButton -and ($e.Current.Name -eq 'Code' -or $e.Current.Name -eq 'Claude')){ try{ if($e.GetCurrentPattern([System.Windows.Automation.SelectionItemPattern]::Pattern).Current.IsSelected){ return $e.Current.Name } }catch{} } } 'Code' }

$w=Win
if(-not $w){ "UI: Claude desktop window not found, skipped"; exit 0 }
try{ $wp=$w.GetCurrentPattern([System.Windows.Automation.WindowPattern]::Pattern); if($wp.Current.WindowVisualState -eq 'Minimized'){ $wp.SetWindowVisualState('Normal'); Start-Sleep -Seconds 1 } }catch{}
$orig=CurMode $w
$archived=0;$deleted=0;$kept=0
function Pass($mode){
  $w=Win; $n=0; $idx=0
  if(-not (Mode $w $mode)){ "ABORT [$mode]: could not switch the app to $mode mode, nothing touched"; return }
  while($n -lt $Max){
    if((CurMode (Win)) -ne $mode){ "ABORT [$mode]: app mode changed underneath, stopping"; return }
    $n++; $w=Win; $rows=@(Rows $w); if($n -eq 1){ "scan [$mode]: $($rows.Count) rows" }
    if($mode -eq 'Code'){ $cand=@($rows | ? { $_.Name -notmatch '\b(Running|Working|Responding|Waiting)\b' -and -not $script:fail.ContainsKey($_.Name) }) }
    else { $cand=@($rows | ? { -not $script:fail.ContainsKey($_.Name) }); if($KeepChats -gt 0){ $cand=@($cand | Select -Skip $KeepChats) } }
    if($cand.Count -eq 0){ break }
    $row=$cand[0]; $title=$row.Name
    if($DryRun){ "DRY  [$mode] would " + $(if($mode -eq 'Code'){'archive'}else{'delete'}) + ": $title"; $script:fail[$title]=1; continue }
    if(-not (OpenMenu $row.More.Current.Name)){ $script:fail[$title]=1; "SKIP [$mode] menu did not open: $title"; continue }
    if($mode -eq 'Code'){
      $base=$row.More.Current.Name -replace '^More options for ',''
      $live=(Kids (Win)) | ? { $_.Current.ControlType -eq [System.Windows.Automation.ControlType]::Button -and $_.Current.Name -like "$base*" -and $_.Current.Name -notlike 'More options*' -and $_.Current.Name -match '\b(Running|Working|Responding|Waiting)\b' } | Select -First 1
      if($live){ Esc; $script:fail[$title]=1; "KEEP (running now): $title"; continue }
      $unpin=MenuItem $w '^Unpin$'
      if($unpin){ Clk $unpin; Start-Sleep -Milliseconds 900; continue }   # row moves; re-scan, next pass archives it
      $it=MenuItem $w '^Archive$'
      if(-not $it){ Esc; $script:fail[$title]=1; "SKIP [Code] no Archive item: $title"; continue }
      Clk $it; Start-Sleep -Milliseconds 1200; $script:archived++; "ARCHIVED: $title"
    } else {
      $it=MenuItem $w '^Delete$'
      if(-not $it){ Esc; $script:fail[$title]=1; "SKIP [Chat] no Delete item: $title"; continue }
      Clk $it; Start-Sleep -Milliseconds 900
      $w=Win; $conf=$null; $dlg=(Kids $w) | ? { $_.Current.ControlType -eq [System.Windows.Automation.ControlType]::Window -and $_.Current.Name -match '^Delete' } | Select -First 1
      # Only a chat-deletion dialog may be confirmed. "Delete session?" (a Code session) is always cancelled.
      if(-not $dlg -or $dlg.Current.Name -match 'session'){ Esc; Start-Sleep -Milliseconds 500; Esc; $script:fail[$title]=1; "SKIP [Chat] unexpected dialog '$($dlg.Current.Name)': $title"; continue }
      foreach($e in (Kids $w)){ if($e.Current.ControlType -eq [System.Windows.Automation.ControlType]::Button -and $e.Current.Name -eq 'Delete' -and $e.Current.BoundingRectangle.X -gt 640){ $conf=$e; break } }
      if(-not $conf){ Esc; $script:fail[$title]=1; "SKIP [Chat] no confirm button: $title"; continue }
      Clk $conf; Start-Sleep -Milliseconds 1200
      $script:deleted++; "DELETED chat: $title"
    }
  }
}
Pass 'Code'
if(-not $NoChats){ Pass 'Claude' }
$w=Win; if($w -and (CurMode $w) -ne $orig){ Mode $w $orig }
"UI done: archived=$archived deleted_chats=$deleted"
