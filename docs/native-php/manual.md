# NativePHP — Selected Documentation

Source: context7 `/nativephp/nativephp.com` (Desktop v2 and Mobile v3 sections).

NativePHP lets you build cross-platform native applications (Desktop and Mobile) with PHP, HTML, CSS, and JavaScript on top of a Laravel application. The runtime wraps the Laravel app and exposes APIs for windows, menus, notifications, dialogs, child processes, the local SQLite database, queues, and packaging/distribution.

## Requirements (Desktop v2)

- PHP 8.3+
- Laravel 11+
- Node 22+
- OS: Windows 10+, macOS 12+, or Linux
- Recommended: PHP and Node installed natively (Laravel Herd works well on macOS/Windows).

## Installation

### Add NativePHP to an Existing Laravel App (Desktop)

```sh
composer require nativephp/electron
php artisan native:install
```

`native:install` is registered as a Composer `post-update-cmd`, so it re-runs on subsequent `composer update`.

### Add NativePHP Mobile to an Existing Laravel App

```bash
composer require nativephp/mobile
php artisan native:jump
```

### Create a New Laravel App With the Mobile Starter

```bash
laravel new my-app --using=nativephp/mobile-starter
cd my-app
php artisan native:jump
```

### Mobile `native:install` Command

```
native:install {platform?}
```

- `platform` — `android`, `ios`, or `both`.
- `--force` / `--fresh` — overwrite existing files.
- `--with-icu` — include ICU support for Android (+~30 MB).
- `--without-icu` — exclude ICU support for Android.
- `--skip-php` — do not download PHP binaries.

### Upgrade Path

```sh
composer update
php artisan native:install
```

## Windows (Desktop)

Use the `Native\Desktop\Facades\Window` facade.

### Open a New Window

```php
namespace App\Providers;

use Native\Desktop\Facades\Window;

class NativeAppServiceProvider
{
    public function boot(): void
    {
        Window::open()
            ->width(800)
            ->height(800);
    }
}
```

The root URL of the Laravel app opens by default in the new window. The fluent builder exposes `width`, `height`, `position`, `title`, `resizable`, `fullscreen`, `alwaysOnTop`, `frameless`, `transparent`, `webPreferences`, and similar Electron-style options.

### Refer to a Specific Window by Name

```php
Window::open('secondary');

// Later — anywhere in the app:
Window::get('secondary')->title('Mmmm... delicious!');
```

The string passed to `open(...)` becomes the window's identifier; `Window::get($id)` returns a manipulator for that window.

## Application Menu

Use the `Native\Desktop\Facades\Menu` facade plus `Menu::make(...)` to assemble the menubar.

```php
use Native\Desktop\Facades\Menu;

Menu::make(
    Menu::fullscreen('Supersize me!'),
    // ...other menu items
);
```

- `Menu::fullscreen(string $label = 'Fullscreen')` — toggles fullscreen for the focused window. Requires the window to be fullscreen-able.

## Context Menu (Renderer Side, JavaScript)

```javascript
Native.contextMenu([
    {
        label: 'Edit',
        accelerator: 'e',
        click(menuItem, window, event) {
            // executed when the menu item is clicked
        },
    },
    // ...
]);
```

## Dialogs

`Native\Desktop\Facades\Dialog` opens native open/save/message dialogs.

```php
Dialog::new()
    ->title('Save a file')
    ->save();
```

`save()` returns the chosen path; it does NOT write the file. Other methods: `open()`, `message()`, `error()`, `confirm()`. Builders expose `filters([['name' => 'Images', 'extensions' => ['png','jpg']]])`, `defaultPath()`, `properties([...])`, etc.

## Child Processes

`ChildProcess` spawns background commands that outlive the originating HTTP request.

```php
use Native\Desktop\Facades\ChildProcess;

ChildProcess::start(
    cmd: 'tail -f storage/logs/laravel.log',
    alias: 'tail',
);
```

- `cmd` — command to execute. Must be on PATH or absolute.
- `alias` — handle used to address the process from elsewhere.

Other helpers: `ChildProcess::php(...)`, `ChildProcess::artisan(...)`, `ChildProcess::stop($alias)`, `ChildProcess::restart($alias)`, `ChildProcess::message($alias, $payload)`.

## Databases

NativePHP ships with SQLite by default. Migrations work like normal Laravel.

```shell
php artisan native:migrate
```

- In production, NativePHP attempts to auto-migrate the user's database copy under their `appdata` directory whenever the app version changes.
- In development, run `native:migrate` manually whenever you change schema. It's equivalent to Laravel's `migrate`.

## Queues

Laravel queues work out of the box. The default driver is the bundled SQLite database; the `jobs` table migration is added automatically by NativePHP. Use normal Laravel job dispatch:

```php
SomeJob::dispatch($payload);
```

A background worker is started by NativePHP automatically; you do not run `queue:work` yourself.

## Notifications

```php
use Native\Desktop\Facades\Notification;

Notification::title('Build complete')
    ->message('The release build finished in 42s.')
    ->show();
```

OS-native toasts appear via the system notification center. On macOS the app must be code-signed and notarized for production delivery of notifications.

## Shortcuts and Global Accelerators

```php
use Native\Desktop\Facades\GlobalShortcut;

GlobalShortcut::key('Cmd+Shift+P')
    ->event(\App\Events\OpenCommandPalette::class)
    ->register();
```

Bind keyboard shortcuts to Laravel events. The event is dispatched on the queue and any listener (UI or backend) can handle it.

## Settings (Persistent Key-Value Store)

```php
use Native\Desktop\Facades\Settings;

Settings::set('theme', 'dark');
$theme = Settings::get('theme', 'light');
```

Settings persist across launches in the user's appdata directory.

## Packaging and Distribution

### Mobile

```shell
php artisan native:package {platform}
```

- `platform` — `android` / `a` / `ios` / `i`.
- Options: build type (debug/release), output directory, platform-specific signing keys.

### Desktop

```shell
php artisan native:build
```

Produces a signed installer for the host OS by default (DMG on macOS, NSIS on Windows, AppImage/deb on Linux). Configure signing in `config/nativephp.php`.

### Auto-Update

NativePHP supports Electron-style auto-updates. Point `nativephp.update_url` at a static manifest hosted on a CDN; the runtime checks at boot and downloads updates atomically.

## NativePHP for Mobile — Quick Reference

- `php artisan native:jump` — refresh and re-deploy the bundled app to the simulator / device.
- `php artisan native:run` — alias for `native:jump` with explicit platform.
- `php artisan native:devices` — list connected simulators / physical devices.
- ICU support adds ~30 MB to Android builds; only include it when you need Unicode collation.

## Best Practices

- Keep all OS-facing API calls (`Window`, `Menu`, `Dialog`, etc.) inside a dedicated `NativeAppServiceProvider` so the rest of the Laravel app remains portable to a normal web deploy.
- Use `Native::isDesktop()` / `Native::isMobile()` to branch behavior across builds when you target both.
- Treat the bundled SQLite database as ephemeral in development — wipe it freely. In production let `native:migrate` handle schema drift.
- Code-sign on macOS and Windows BEFORE the first public release. Signing setup is irreversible to remove once users have downloaded; mid-stream signing changes can break update flows.
- For long-running work, prefer `ChildProcess::php()` or `ChildProcess::artisan()` over inline blocking work in HTTP requests; the renderer must stay responsive.
- For mobile builds, prefer `--skip-php` when iterating quickly and re-add the PHP binary downloads only before packaging.

## Common Commands Summary

| Command | Purpose |
| --- | --- |
| `native:install` | Set up NativePHP after `composer require` |
| `native:jump` | Mobile dev refresh / deploy |
| `native:migrate` | Run Laravel migrations against the bundled SQLite |
| `native:serve` | Run the desktop runtime in dev mode |
| `native:build` | Build the desktop installer |
| `native:package` | Package the mobile app |
| `native:devices` | List mobile devices/simulators |
