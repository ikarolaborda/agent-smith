# Laravel 13 — Selected Documentation

Source: context7 `/laravel/docs` (branch 13.x), `/laravel/docs/llms.txt`

## Resource Controller with Constructor Dependency Injection

Controller class demonstrating constructor dependency injection, index method with eager loading and pagination, store method with request validation, and route model binding.

```php
<?php

namespace App\Http\Controllers;

use App\Models\User;
use Illuminate\Http\Request;
use Illuminate\Http\RedirectResponse;
use Illuminate\View\View;

class UserController extends Controller
{
    public function __construct(
        protected UserRepository $users,
    ) {}

    public function index(): View
    {
        $users = User::with('posts')->paginate(15);
        return view('users.index', compact('users'));
    }

    public function store(Request $request): RedirectResponse
    {
        $validated = $request->validate([
            'name' => 'required|string|max:255',
            'email' => 'required|email|unique:users',
            'password' => 'required|min:8|confirmed',
        ]);

        $user = User::create($validated);

        return redirect()->route('users.show', $user)
            ->with('success', 'User created successfully.');
    }

    public function show(User $user): View
    {
        return view('users.show', compact('user'));
    }
}
```

## Generating Eloquent Models with Artisan

```shell
php artisan make:model Flight
php artisan make:model Flight --migration
php artisan make:model Flight -m
```

## Eloquent ORM Models with Relationships, Casts, and Scopes

```php
<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\HasMany;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\SoftDeletes;

class Post extends Model
{
    use HasFactory, SoftDeletes;

    protected $fillable = ['title', 'content', 'user_id', 'published_at'];

    protected $casts = [
        'published_at' => 'datetime',
        'metadata' => 'array',
    ];

    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }

    public function comments(): HasMany
    {
        return $this->hasMany(Comment::class);
    }

    public function scopePublished($query)
    {
        return $query->whereNotNull('published_at')
                     ->where('published_at', '<=', now());
    }
}

$posts = Post::with('user', 'comments')
    ->published()
    ->orderBy('published_at', 'desc')
    ->paginate(10);

$post = Post::create([
    'title' => 'Hello World',
    'content' => 'My first post',
    'user_id' => auth()->id(),
]);

$post->update(['title' => 'Updated Title']);
$post->delete();
```

## Form Request Validation Rules

The `rules()` method on a `FormRequest` defines validation for incoming requests. Dependencies can be type-hinted and resolved by the service container.

```php
/**
 * @return array<string, \Illuminate\Contracts\Validation\ValidationRule|array<mixed>|string>
 */
public function rules(): array
{
    return [
        'title' => ['required', 'unique:posts', 'max:255'],
        'body' => ['required'],
    ];
}
```

## Laravel 13 PHP Attributes for Controllers

Laravel 13 leverages PHP attributes (`#[Middleware]`, `#[Authorize]`) to declare controller middleware and policy checks directly on classes and methods, co-locating configuration with the affected code.

```php
<?php

namespace App\Http\Controllers;

use App\Models\Comment;
use App\Models\Post;
use Illuminate\Routing\Attributes\Controllers\Authorize;
use Illuminate\Routing\Attributes\Controllers\Middleware;

#[Middleware('auth')]
class CommentController
{
    #[Middleware('subscribed')]
    #[Authorize('create', [Comment::class, 'post'])]
    public function store(Post $post)
    {
        // ...
    }
}
```

## Routing Basics

- Define routes in `routes/web.php` and `routes/api.php`.
- Resource routes via `Route::resource('posts', PostController::class)`.
- Route model binding resolves `User $user` from `{user}` segments automatically.
- Named routes via `->name('users.show')`; reference with `route('users.show', $user)`.

## Migrations

```shell
php artisan make:migration create_posts_table
php artisan migrate
php artisan migrate:rollback
```

```php
public function up(): void
{
    Schema::create('posts', function (Blueprint $table) {
        $table->id();
        $table->foreignId('user_id')->constrained()->cascadeOnDelete();
        $table->string('title');
        $table->text('content');
        $table->timestamp('published_at')->nullable();
        $table->timestamps();
        $table->softDeletes();
    });
}
```

## Best Practices

- Prefer invokable controllers for single-action endpoints.
- Use Form Requests for non-trivial validation; keep controllers thin.
- Co-locate authorization with `#[Authorize]` attributes or Policies.
- Prefer explicit property assignment + `save()` over mass assignment when reasoning about side effects matters.
- Use eager loading (`with()`) to avoid N+1.
- Use named queues and supervisor-monitored workers for asynchronous jobs.
- Tests: Pest/PHPUnit + `RefreshDatabase` trait; use factories with states for fixture data.
