#!/usr/bin/env ruby
require 'xcodeproj'
root = File.expand_path('..', __dir__)
path = File.join(root, 'ios/Trill.xcodeproj')
project = Xcodeproj::Project.new(path)
project.root_object.attributes['BuildIndependentTargetsInParallel'] = '1'
project.root_object.attributes['LastUpgradeCheck'] = '2710'
project.build_configurations.each do |c|
  c.build_settings.merge!({
    'ASSETCATALOG_COMPILER_GENERATE_SWIFT_ASSET_SYMBOL_EXTENSIONS' => 'YES',
    'DEAD_CODE_STRIPPING' => 'YES',
    'STRING_CATALOG_GENERATE_SYMBOLS' => 'YES'
  })
end
app = project.new_target(:application, 'Trill', :ios, '18.0')
app.product_name = 'Trill'
version_config = project.main_group.new_file('Version.xcconfig')
app.build_configurations.each do |c|
  c.base_configuration_reference = version_config
  c.build_settings.merge!({
    'PRODUCT_BUNDLE_IDENTIFIER' => 'io.bcook.trill', 'SWIFT_VERSION' => '5.0',
    'TARGETED_DEVICE_FAMILY' => '1,2', 'GENERATE_INFOPLIST_FILE' => 'YES',
    'INFOPLIST_KEY_CFBundleDisplayName' => 'Trill', 'INFOPLIST_KEY_UILaunchScreen_Generation' => 'YES',
    'INFOPLIST_KEY_UIApplicationSceneManifest_Generation' => 'YES',
    'INFOPLIST_KEY_UISupportedInterfaceOrientations' => 'UIInterfaceOrientationPortrait UIInterfaceOrientationLandscapeLeft UIInterfaceOrientationLandscapeRight UIInterfaceOrientationPortraitUpsideDown',
    'INFOPLIST_KEY_NSLocalNetworkUsageDescription' => 'Trill connects directly to your computer to manage sessions and open terminals.',
    'INFOPLIST_FILE' => 'Trill/Info.plist',
    'CODE_SIGN_STYLE' => 'Automatic', 'ENABLE_USER_SCRIPT_SANDBOXING' => 'YES',
    'ASSETCATALOG_COMPILER_APPICON_NAME' => 'AppIcon'
  })
end
sources = project.main_group.new_group('Trill', 'Trill')
Dir.glob(File.join(root, 'ios/Trill/*.swift')).sort.each { |f| app.source_build_phase.add_file_reference(sources.new_file(File.basename(f))) }
resources = sources.new_file('Assets.xcassets'); app.resources_build_phase.add_file_reference(resources)
sources.new_file('Info.plist')
%w[PrivacyInfo.xcprivacy PrivacyPolicy.txt Acknowledgements.txt].each { |f| app.resources_build_phase.add_file_reference(sources.new_file(f)) }
[['SwiftTerm', '../vendor/SwiftTerm']].each do |name,path|
  pkg = project.new(Xcodeproj::Project::Object::XCLocalSwiftPackageReference)
  pkg.relative_path = path
  project.root_object.package_references << pkg
  dep = project.new(Xcodeproj::Project::Object::XCSwiftPackageProductDependency)
  dep.package = pkg; dep.product_name = name; app.package_product_dependencies << dep
  bf = project.new(Xcodeproj::Project::Object::PBXBuildFile); bf.product_ref = dep; app.frameworks_build_phase.files << bf
end
pkg = project.new(Xcodeproj::Project::Object::XCLocalSwiftPackageReference)
pkg.relative_path = '../vendor/swift-mosh'
project.root_object.package_references << pkg
dep = project.new(Xcodeproj::Project::Object::XCSwiftPackageProductDependency)
dep.package = pkg; dep.product_name = 'MoshCore'; app.package_product_dependencies << dep
bf = project.new(Xcodeproj::Project::Object::PBXBuildFile); bf.product_ref = dep; app.frameworks_build_phase.files << bf
[:unit_test_bundle, :ui_test_bundle].each do |kind|
  name = kind == :unit_test_bundle ? 'TrillTests' : 'TrillUITests'
  target = project.new_target(kind, name, :ios, '18.0')
  target.add_dependency(app)
  group = project.main_group.new_group(name, name)
  Dir.glob(File.join(root, "ios/#{name}/*.swift")).sort.each { |f| target.source_build_phase.add_file_reference(group.new_file(File.basename(f))) }
  target.build_configurations.each do |c|
    c.build_settings.merge!({'PRODUCT_BUNDLE_IDENTIFIER' => "io.bcook.trill.#{kind == :unit_test_bundle ? 'tests' : 'uitests'}", 'SWIFT_VERSION' => '5.0', 'GENERATE_INFOPLIST_FILE' => 'YES', 'CODE_SIGN_STYLE' => 'Automatic'})
    if kind == :unit_test_bundle
      c.build_settings['TEST_HOST'] = '$(BUILT_PRODUCTS_DIR)/Trill.app/Trill'
      c.build_settings['BUNDLE_LOADER'] = '$(TEST_HOST)'
    else
      c.build_settings['TEST_TARGET_NAME'] = 'Trill'
    end
  end
end
project.save
scheme = Xcodeproj::XCScheme.new
scheme.add_build_target(app)
scheme.set_launch_target(app)
project.targets.select { |t| t.name.end_with?('Tests') }.each { |t| scheme.add_test_target(t) }
scheme.save_as(path, 'Trill', true)
